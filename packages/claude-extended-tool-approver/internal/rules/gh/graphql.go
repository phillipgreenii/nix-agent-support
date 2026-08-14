package gh

import "strings"

// GRAPHQL DOCUMENT CLASSIFICATION (pg2-44dsd)
//
// WHY THIS EXISTS. `gh api graphql` is how BOTH GraphQL reads and GraphQL mutations
// arrive, and gh sends either as an HTTP POST — measured in api.go's spelling table. So
// pg2-cl0v2's method-keyed apiVerdict, which is the right reading of the REST surface,
// answers Ask for a read-only GraphQL query as well. That was the correct conservative
// default; what it costs is one Ask on every GraphQL read.
//
// MEASURED BEFORE ANY CODE WAS WRITTEN, which is the bead's FIRST acceptance criterion and
// the reason the alternative outcome (close as not-worth-it) was on the table. Corpus:
// `~/.local/share/claude-extended-tool-approver/asks.db`, table `tool_decisions`, ids
// 1..347919 (2026-03-13..2026-08-14), read via `?immutable=1`. 587 non-excluded `Bash`
// rows whose `$.command` contains `api graphql` were replayed through the binary built
// from a064a73e:
//
//	576 return Ask, ALL 576 carrying the SAME reason — pg2-cl0v2's mutating-method Ask.
//	569 of those 576 had been logged `allow` before pg2-cl0v2 landed, so this is an
//	    allow->ask regression and not a pre-existing Ask.
//	 11 are prose (`bd create "…gh api graphql…"`) and correctly reach no gh branch —
//	    which is the pg2-5b901 property already holding, measured here in passing.
//
// Classifying the argv-visible document of those 576:
//
//	236  bare `{ … }`, NO operation keyword    READ
//	142  explicit `query` keyword              READ
//	188  `mutation` keyword                    WRITE
//	  9  `-F query=@file` or `--input`         NOT ARGV-VISIBLE
//	  1  no `query=` parameter at all          UNREADABLE
//
// 378 of 576 (66%) are therefore reads paying an Ask, spread across 321 DISTINCT command
// texts — not one command in a loop. That is the evidence that built this.
//
// THE 236-ROW MAJORITY IS EXACTLY WHY THE KEYWORD IS NOT THE TEST. GraphQL's operation
// keyword is OPTIONAL: a bare `{ viewer { login } }` IS a query. Keying on the word
// `query` would have missed nearly two thirds of the measured win.
//
// AND IT IS WHY THIS IS A DEFINITION-LEVEL SCANNER AND NOT A SUBSTRING TEST. Keying on the
// substring `mutation` is the pg2-5b901 text-matching failure mode. MEASURED, gh 2.97.0
// (nixpkgs), 2026-08-14, via `gh api --hostname no-such-host.invalid --verbose`:
//
//	graphql -f 'query={ repository(owner:"o",name:"r") { mutation } }'
//	  -> body {"query":"{ repository(owner:\"o\",name:\"r\") { mutation } }"}
//
// — a document whose SELECTION FIELD is named `mutation` and which mutates nothing. The
// scanner sees that name at brace depth 2, never at the definition level, so it classifies
// READ structurally rather than by an exclusion list.
//
// FAIL-SAFE IS THE ZERO VALUE. Every shape this does not fully understand — an
// unterminated string, unbalanced braces, a top-level word that is not an operation
// keyword, a document that is not in argv at all — lands on graphqlOpaque, which the
// caller reads as "NOT PROVEN A READ" and answers with the incumbent Ask. Nothing here can
// produce an Approve the pre-pg2-44dsd code did not already produce for an effective GET.
//
// `operationName` IS DELIBERATELY NOT MODELLED. A document may hold several operations and
// `--field operationName=X` picks the one that runs, so a document containing [query A,
// mutation B] with `operationName=A` really does only read. This classifier answers WRITE
// for ANY document containing a mutation, which for that spelling costs an Ask on a read —
// the safe direction, and zero rows in the corpus above. Modelling the selection would
// mean resolving a body parameter into a document position in order to become LESS
// restrictive, which is the one direction a parsing bug must never move; the measured win
// does not need it.
//
// WHAT WOULD JUSTIFY EXTENDING IT: observed friction in the decision log on a multi-
// operation document with `operationName`, which is a re-measurement of the same corpus
// query, not a redesign.

// graphqlDocKind is how much a GraphQL document could be shown to be.
type graphqlDocKind int

const (
	// graphqlOpaque means the document could not be READ as GraphQL — it was never in
	// argv, or it did not scan. It is the ZERO VALUE on purpose: a graphqlDoc nobody
	// filled in is opaque, so a missing classification can never read as a read.
	graphqlOpaque graphqlDocKind = iota
	// graphqlRead means the document scanned cleanly and EVERY operation definition in it
	// is a query (whether written with the `query` keyword or as the bare `{ … }`
	// shorthand).
	graphqlRead
	// graphqlWrite means the document scanned cleanly and at least one operation
	// definition is a `mutation` or a `subscription`.
	//
	// A SUBSCRIPTION IS COUNTED AS A WRITE, which is conservative rather than accurate: a
	// subscription reads. It is grouped here because `gh api graphql` cannot serve one
	// over a single HTTP POST anyway, so the classification is inert, and the alternative
	// — a third kind nothing distinguishes — would only widen the Approve surface for a
	// shape that cannot run.
	graphqlWrite
)

// graphqlDoc is what one scan of a GraphQL document establishes.
type graphqlDoc struct {
	Kind graphqlDocKind
	// Names is every GraphQL NAME token the scan saw, outside string literals and
	// comments. It is a TOKEN set, not a text search: a name inside `"…"` or after `#` is
	// not in it, which is the pg2-5b901 discipline the Kind classification already
	// follows. It exists for the pinned root-field questions in api.go.
	Names map[string]bool
}

// graphqlPullRequestCreateFields are the GraphQL mutation root fields that CREATE a pull
// request. `createPullRequest` is the whole set on GitHub's schema today.
//
// DELIBERATELY NOT IN IT: `mergePullRequest` and `markPullRequestReadyForReview`, whose
// porcelain equivalents are the `gh pr merge` Reject and the `gh pr ready` Ask. api.go's
// IsPullRequestMerge doc already records that widening the merge control to a `graphql`
// mutation needs an OPERATOR RULING and not a tidy-up, and that boundary is unchanged
// here; both reach the generic mutation Ask. Measured in the corpus above: zero rows for
// either, and zero for `createPullRequest` itself.
var graphqlPullRequestCreateFields = map[string]bool{"createPullRequest": true}

// CreatesPullRequest reports whether the document names a pull-request-creating mutation
// root field.
//
// It is a NAME-TOKEN membership test, not a root-field resolution, and the difference only
// ever errs toward MORE restriction: a document that names `createPullRequest` as an
// alias, a variable or a fragment gets the pinned Ask instead of a possible Approve, and
// such a document is one GitHub's schema rejects anyway (the field exists only on
// Mutation). Resolving true root fields would need the schema, and it would be spent
// making the rule LESS restrictive.
func (d graphqlDoc) CreatesPullRequest() bool {
	for field := range graphqlPullRequestCreateFields {
		if d.Names[field] {
			return true
		}
	}
	return false
}

// classifyGraphQLDocument scans src as a GraphQL executable document. src MUST be the
// document text as it appeared in argv, already unquoted by cmdparse — this function makes
// no quoting decision and holds no shell state.
func classifyGraphQLDocument(src string) graphqlDoc {
	s := &graphqlScanner{src: src, names: map[string]bool{}}
	kinds, ok := s.scanDefinitions()
	doc := graphqlDoc{Names: s.names}
	if !ok || len(kinds) == 0 {
		// A scan failure, or a document with no OPERATION at all (empty, or only fragment
		// definitions). Neither can be shown to read, so both stay opaque.
		return doc
	}
	for _, k := range kinds {
		if k == graphqlWrite {
			doc.Kind = graphqlWrite
			return doc
		}
	}
	doc.Kind = graphqlRead
	return doc
}

// graphqlScanner walks a GraphQL document once, at DEFINITION level.
//
// It is a scanner over one already-tokenized, already-unquoted argument — the same
// pg2-x9452 Guard 2 false positive cmdparse's HasShortFlag, parseGhAPICall and
// ghCommandWordIndexes each record. No lexical decision about the SHELL is made here; the
// only lexing is of GraphQL's own comments and string literals, which is precisely what
// makes it not a substring test.
type graphqlScanner struct {
	src   string
	i     int
	names map[string]bool
}

// scanDefinitions reads the document's top-level definitions and returns one kind per
// OPERATION definition, in order. ok is false as soon as anything does not scan; the
// caller must then treat the whole document as opaque rather than trusting a partial read.
func (s *graphqlScanner) scanDefinitions() (kinds []graphqlDocKind, ok bool) {
	for {
		s.skipTrivia()
		if s.i >= len(s.src) {
			return kinds, true
		}
		c := s.src[s.i]

		// The SHORTHAND anonymous query — `{ viewer { login } }`. GraphQL's operation
		// keyword is optional and this is the form 236 of the 576 measured rows use, so it
		// is the first case rather than an afterthought.
		if c == '{' {
			if !s.skipBalanced() {
				return nil, false
			}
			kinds = append(kinds, graphqlRead)
			continue
		}

		if !isGraphQLNameStart(c) {
			return nil, false // not a definition: `$`, `)`, a stray `@`, a leading `@file`
		}
		word := s.readName()
		var kind graphqlDocKind
		isOperation := true
		switch word {
		case "query":
			kind = graphqlRead
		case "mutation", "subscription":
			kind = graphqlWrite
		case "fragment":
			// A fragment definition is not an operation: it contributes a selection set
			// that some operation must spread. It is scanned (its names count) but adds no
			// kind, so a document of fragments ALONE has no operation and stays opaque.
			isOperation = false
		default:
			// Any other top-level word is a type-system definition (`type`, `schema`,
			// `enum`, `extend`) or a typo. `gh api graphql` sends executable documents, so
			// this is not a shape to read leniently.
			return nil, false
		}
		// The definition HEADER — operation name, variable definitions, directives — sits
		// between the keyword and the selection set.
		if !s.skipDefinitionHeader() {
			return nil, false
		}
		if !s.skipBalanced() {
			return nil, false
		}
		if isOperation {
			kinds = append(kinds, kind)
		}
	}
}

// skipDefinitionHeader advances from just after a definition keyword to the `{` that opens
// its selection set, leaving the scanner ON that brace. It returns false if the selection
// set never opens.
//
// Variable definitions and directive arguments are consumed as BALANCED groups, which is
// what keeps a default value's object literal — `query Q($f: Filter = {a: 1}) { … }` —
// from being mistaken for the selection set. Every `{` in a header is inside such a group,
// because GraphQL admits a value only inside a parenthesised argument list.
func (s *graphqlScanner) skipDefinitionHeader() bool {
	for {
		s.skipTrivia()
		if s.i >= len(s.src) {
			return false
		}
		c := s.src[s.i]
		switch {
		case c == '{':
			return true
		case c == '(' || c == '[':
			if !s.skipBalanced() {
				return false
			}
		case c == '"':
			if !s.skipString() {
				return false
			}
		case c == '}' || c == ')' || c == ']':
			return false // a closer before the selection set ever opened
		case isGraphQLNameStart(c):
			s.names[s.readName()] = true
		default:
			s.i++ // `:`  `!`  `=`  `$`  `@`  `|`  `&` — the header's punctuation
		}
	}
}

// skipBalanced consumes the delimiter group starting at the scanner's current position —
// which MUST be `{`, `(` or `[` — through its matching closer, and returns false if the
// group does not close or closes with the wrong delimiter.
//
// ONE stack covers braces, parens and brackets together, and that is the load-bearing
// detail: an argument list may contain an input-object literal
// (`createPullRequest(input: {draft: true})`), so brace depth alone would mis-track the
// selection-set nesting. Names are collected as they pass, and strings and comments are
// skipped rather than scanned, so no name inside either is ever recorded.
func (s *graphqlScanner) skipBalanced() bool {
	var stack []byte
	for s.i < len(s.src) {
		c := s.src[s.i]
		switch {
		case c == '#':
			s.skipComment()
		case c == '"':
			if !s.skipString() {
				return false
			}
		case c == '{' || c == '(' || c == '[':
			stack = append(stack, c)
			s.i++
		case c == '}' || c == ')' || c == ']':
			if len(stack) == 0 || graphqlCloser(stack[len(stack)-1]) != c {
				return false
			}
			stack = stack[:len(stack)-1]
			s.i++
			if len(stack) == 0 {
				return true
			}
		case isGraphQLNameStart(c):
			s.names[s.readName()] = true
		default:
			s.i++
		}
	}
	return false // ran out of document with the group still open
}

// skipString consumes a GraphQL string literal, block (`"""…"""`) or ordinary (`"…"`),
// starting at the opening quote. It returns false on an unterminated literal, which makes
// the whole document opaque — the safe direction, since an unterminated quote means the
// rest of the text is not selection syntax and must not be read as if it were.
//
// A block string containing the escaped sequence `\"""` closes early here. That is
// accepted: it can only END the string sooner, so the remaining text is scanned as syntax
// and will almost always fail to balance, which is again opaque and therefore Ask.
func (s *graphqlScanner) skipString() bool {
	const block = `"""`
	if strings.HasPrefix(s.src[s.i:], block) {
		end := strings.Index(s.src[s.i+len(block):], block)
		if end < 0 {
			return false
		}
		s.i += len(block) + end + len(block)
		return true
	}
	s.i++ // the opening quote
	for s.i < len(s.src) {
		switch s.src[s.i] {
		case '\\':
			s.i += 2 // an escape and the character it escapes; overshoot ends the loop
		case '"':
			s.i++
			return true
		case '\n':
			return false // an ordinary GraphQL string may not span a line
		default:
			s.i++
		}
	}
	return false
}

// skipComment consumes a `#` comment through the end of its line. The newline itself is
// left for skipTrivia, so a comment on the last line simply ends the document.
func (s *graphqlScanner) skipComment() {
	for s.i < len(s.src) && s.src[s.i] != '\n' {
		s.i++
	}
}

// skipTrivia consumes GraphQL's ignored tokens: whitespace, the comma GraphQL treats as
// whitespace, a UTF-8 byte-order mark, and `#` comments.
func (s *graphqlScanner) skipTrivia() {
	// Written as an escape, not as the character: a literal U+FEFF in Go source is an
	// "illegal byte order mark" and will not compile.
	const bom = "\uFEFF"
	for s.i < len(s.src) {
		switch c := s.src[s.i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == ',':
			s.i++
		case c == '#':
			s.skipComment()
		case strings.HasPrefix(s.src[s.i:], bom):
			s.i += len(bom)
		default:
			return
		}
	}
}

// readName consumes one GraphQL Name. It MUST be called with the scanner on a name-start
// byte, so it always advances.
func (s *graphqlScanner) readName() string {
	start := s.i
	for s.i < len(s.src) && isGraphQLNameContinue(s.src[s.i]) {
		s.i++
	}
	return s.src[start:s.i]
}

// isGraphQLNameStart and isGraphQLNameContinue implement GraphQL's Name production,
// `/[_A-Za-z][_0-9A-Za-z]*/`. They are ASCII by specification, not by simplification: a
// GraphQL Name cannot contain a non-ASCII character, so a multi-byte rune is never part of
// one and the byte-wise reading cannot split one.
func isGraphQLNameStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isGraphQLNameContinue(c byte) bool {
	return isGraphQLNameStart(c) || (c >= '0' && c <= '9')
}

// graphqlCloser maps an opening delimiter to its closer, and returns 0 for anything else
// so a mismatched pair fails the balance check rather than silently closing.
func graphqlCloser(open byte) byte {
	switch open {
	case '{':
		return '}'
	case '(':
		return ')'
	case '[':
		return ']'
	}
	return 0
}
