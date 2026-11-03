package query

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/wire"
)

// MinSubstringChars is the shortest substring query the trigram index serves.
const MinSubstringChars = 3

// SearchRequest is the search operation's body.
type SearchRequest struct {
	Op     string   `json:"op,omitempty"`
	Mode   string   `json:"mode,omitempty"` // word (default) or substring
	Q      string   `json:"q"`
	Where  *Filter  `json:"where,omitempty"`
	Fields []string `json:"fields,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	// OldGenerations includes records of superseded generations.
	OldGenerations bool `json:"old_generations,omitempty"`
}

// Hit is one search hit.
type Hit struct {
	Record    RecordRef `json:"record"`
	SessionID string    `json:"session_id"`
	Time      string    `json:"time"`
	Field     string    `json:"field"`
	Score     float64   `json:"score,omitempty"`
	MatchAt   *int      `json:"match_at,omitempty"`
	Snippet   string    `json:"snippet"`
}

// SearchAnswer is the search answer.
type SearchAnswer struct {
	AsOf     string `json:"as_of"`
	Hits     []Hit  `json:"hits"`
	More     bool   `json:"more"`
	Degraded bool   `json:"degraded,omitempty"`
	// TooShort is true when a substring query was under three characters: the
	// trigram index cannot answer it, so no hits are returned.
	TooShort bool   `json:"too_short,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Search runs a word or substring search.
func (e *Engine) Search(ctx context.Context, req SearchRequest) (*SearchAnswer, *wire.Error) {
	mode := req.Mode
	if mode == "" {
		mode = "word"
	}
	if mode != "word" && mode != "substring" {
		return nil, badf("mode %q: use word or substring", mode)
	}
	tx, err := e.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, wire.FromErr(err)
	}
	defer func() { _ = tx.Rollback() }()
	ans := &SearchAnswer{AsOf: e.asOf(tx), Hits: []Hit{}}
	if e.Behind != nil && e.Behind() {
		ans.Degraded = true
	}
	var match, table string
	var terms []string
	if mode == "word" {
		q, ok := wordQuery(req.Q)
		if !ok {
			ans.Note = "the query has no words"
			return ans, nil
		}
		match, table, terms = q, "fts_word", tokens(req.Q)
	} else {
		if utf8.RuneCountInString(req.Q) < MinSubstringChars {
			ans.TooShort = true
			ans.Note = "a substring query needs at least 3 characters; nothing was searched"
			return ans, nil
		}
		match, table = `"`+strings.ReplaceAll(req.Q, `"`, `""`)+`"`, "fts_tri"
	}
	b := e.builder()
	wsql, wargs, werr := b.where(req.Where)
	if werr != nil {
		return nil, werr
	}
	e.noteFilter("search", b)
	var fieldSQL string
	var fieldArgs []any
	if len(req.Fields) > 0 {
		ph := make([]string, len(req.Fields))
		for i, f := range req.Fields {
			stored, kind, err := b.resolve(f)
			if err != nil {
				return nil, err
			}
			if !kind.Searchable() {
				return nil, badf("field %q is not a text or opaque field", f)
			}
			ph[i] = "?"
			fieldArgs = append(fieldArgs, stored)
		}
		fieldSQL = " AND t.field IN (" + strings.Join(ph, ",") + ")"
	}
	order := "t.id"
	score := "0"
	if mode == "word" {
		order, score = "bm25(fts_word)", "bm25(fts_word)"
	}
	gen := ""
	if !req.OldGenerations {
		gen = " AND r.gen=fl.cur_gen"
	}
	q := "SELECT t.field, r.id, r.file_id, r.gen, r.off, r.item, r.len, r.source, r.session, r.time_ns, " + score +
		" FROM " + table + " JOIN texts t ON t.id=" + table + ".rowid JOIN records r ON r.id=t.rec_id JOIN files fl ON fl.id=r.file_id" +
		gen + " " + strings.Join(b.joins, " ") +
		" WHERE " + table + " MATCH ?" + fieldSQL + " AND (" + wsql + ") ORDER BY " + order
	args := append(append([]any{}, b.joinArgs...), match)
	args = append(append(args, fieldArgs...), wargs...)
	rs, qerr := tx.Query(q, args...)
	if qerr != nil {
		return nil, wire.FromErr(qerr)
	}
	defer func() { _ = rs.Close() }()
	limit := limitOf(req.Limit)
	for rs.Next() {
		var field, src, session string
		var recID, fid, off, tns int64
		var gen, item, ln int
		var sc float64
		if err := rs.Scan(&field, &recID, &fid, &gen, &off, &item, &ln, &src, &session, &tns, &sc); err != nil {
			return nil, wire.FromErr(err)
		}
		var value string
		if e.Res != nil {
			if v, err := e.Res.Value(fid, gen, off, ln, src, item, field); err == nil {
				value = v
			} else if mode == "substring" {
				continue // cannot verify against the copy
			}
		}
		h := Hit{Record: RecordRef{File: FileRef(fid), Gen: gen, Offset: off, Item: item}, SessionID: session, Time: rfc(tns), Field: field}
		if mode == "substring" {
			at := strings.Index(value, req.Q)
			if at < 0 {
				continue // the index matched trigrams but the bytes do not hold the text
			}
			h.MatchAt = &at
			h.Snippet = window(value, at, at+len(req.Q))
		} else {
			h.Score = -sc
			h.Snippet = wordSnippet(value, terms)
		}
		if len(ans.Hits) == limit {
			ans.More = true
			break
		}
		ans.Hits = append(ans.Hits, h)
	}
	return ans, nil
}

// window returns the text around [from,to) with ellipses where it is cut.
func window(v string, from, to int) string {
	const before, after = 40, 60
	s, e := from-before, to+after
	pre, post := "…", "…"
	if s <= 0 {
		s, pre = 0, ""
	}
	if e >= len(v) {
		e, post = len(v), ""
	}
	for s > 0 && !utf8.RuneStart(v[s]) {
		s++
	}
	for e < len(v) && !utf8.RuneStart(v[e]) {
		e--
	}
	return pre + v[s:e] + post
}

type span struct{ s, e int }

// wordSnippet brackets the matched words (a run of matched words separated by
// whitespace shares one pair of brackets) in a window around the first match.
func wordSnippet(v string, terms []string) string {
	if v == "" {
		return ""
	}
	want := map[string]bool{}
	for _, t := range terms {
		want[t] = true
	}
	var words []span
	start := -1
	for i, r := range v {
		if isWordRune(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			words = append(words, span{start, i})
			start = -1
		}
	}
	if start >= 0 {
		words = append(words, span{start, len(v)})
	}
	var hits []span
	for _, w := range words {
		if want[strings.ToLower(v[w.s:w.e])] {
			if n := len(hits); n > 0 && strings.TrimSpace(v[hits[n-1].e:w.s]) == "" {
				hits[n-1].e = w.e
			} else {
				hits = append(hits, w)
			}
		}
	}
	if len(hits) == 0 {
		return window(v, 0, 0)
	}
	const before, after = 40, 80
	ws, we := hits[0].s-before, hits[0].e+after
	pre, post := "…", "…"
	if ws <= 0 {
		ws, pre = 0, ""
	}
	if we >= len(v) {
		we, post = len(v), ""
	}
	for ws > 0 && !utf8.RuneStart(v[ws]) {
		ws++
	}
	for we < len(v) && !utf8.RuneStart(v[we]) {
		we--
	}
	var b strings.Builder
	b.WriteString(pre)
	pos := ws
	for _, h := range hits {
		if h.s < ws || h.e > we {
			continue
		}
		b.WriteString(v[pos:h.s])
		b.WriteString("[" + v[h.s:h.e] + "]")
		pos = h.e
	}
	b.WriteString(v[pos:we])
	b.WriteString(post)
	return b.String()
}
