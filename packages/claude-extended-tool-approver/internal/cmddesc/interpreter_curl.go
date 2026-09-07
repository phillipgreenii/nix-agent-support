package cmddesc

import (
	"fmt"
	"strings"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmdparse"
)

// The flag spellings whose PRESENCE changes what curl's URLs mean — data or
// form bodies (content leaves), uploads (content leaves), an explicit
// request method, and HEAD. The schema still owns each flag's arity and
// operand role (a `-d @file` read comes from the generic scan); these lists
// only say which spellings steer the net effect's direction and method.
var (
	curlDataFlags   = []string{"-d", "--data", "--data-binary", "--data-raw", "--data-urlencode", "-F", "--form"}
	curlUploadFlags = []string{"-T", "--upload-file"}
	curlMethodFlags = []string{"-X", "--request"}
	curlHeadFlags   = []string{"-I", "--head"}
	curlGetFlags    = []string{"-G", "--get"}
	curlURLFlags    = []string{"--url"}
)

// curlInterpreter runs the generic scan and then derives ONE EffectNet per
// URL (positionals plus --url values): Host from the URL, Direction outbound
// when any data/form/upload flag was seen or -X names a method other than
// GET/HEAD/OPTIONS (inbound otherwise), Method from -X, else GET under -G,
// else POST with data or form flags, PUT with an upload, GET otherwise; -I
// forces HEAD (and, absent data flags, an inbound direction). A URL
// that is a runtime expansion is a dynamic net effect; no URL at all is
// insufficient.
type curlInterpreter struct{}

// Interpret implements Interpreter.
func (curlInterpreter) Interpret(leaf cmdparse.ParsedCommand, schema CommandSchema, ctx Context) Interpretation {
	st := scan(leaf, schema, ctx)
	st.finish()
	if !st.scanned {
		return st.result()
	}
	urls := append(st.positionals(), st.flagValues(curlURLFlags...)...)
	if len(urls) == 0 {
		st.fail("no URL operand")
		return st.result()
	}

	data := st.anyFlagSeen(curlDataFlags...)
	upload := st.anyFlagSeen(curlUploadFlags...)
	method := ""
	if vals := st.flagValues(curlMethodFlags...); len(vals) > 0 {
		last := vals[len(vals)-1]
		if st.leaf.ArgIsLiveExpansion(last.idx) {
			st.fail("request method at arg %d is a runtime expansion", last.idx)
		}
		method = strings.ToUpper(last.tok)
	}
	switch {
	case st.anyFlagSeen(curlHeadFlags...):
		method = "HEAD"
	case method != "":
	case st.anyFlagSeen(curlGetFlags...):
		// -G moves the data into the query string: the method is GET but the
		// data still leaves, so the direction below stays outbound.
		method = "GET"
	case data:
		method = "POST"
	case upload:
		method = "PUT"
	default:
		method = "GET"
	}
	direction := NetInbound
	if data || upload || !readOnlyMethod(method) {
		direction = NetOutbound
	}

	for _, u := range urls {
		e := Effect{Kind: EffectNet, Direction: direction, Method: method, Source: fmt.Sprintf("arg %d", u.idx)}
		if st.leaf.ArgIsLiveExpansion(u.idx) {
			e.Host, e.Dynamic = u.tok, true
		} else {
			host, ok := urlHost(u.tok)
			if !ok {
				st.fail("cannot determine the host of URL %q at arg %d", u.tok, u.idx)
			}
			e.Host = host
		}
		st.effects = append(st.effects, e)
	}
	return st.result()
}

// readOnlyMethod reports whether an HTTP method carries no request content by
// convention. Anything unrecognised is NOT read-only (fail-closed).
func readOnlyMethod(m string) bool {
	switch m {
	case "GET", "HEAD", "OPTIONS":
		return true
	default:
		return false
	}
}

// urlHost extracts the lower-cased host of a URL: the authority after
// `scheme://` (or, without a scheme, the text up to the first `/`), minus
// userinfo, port and any path/query/fragment. A bracketed IPv6 literal keeps
// its address without the brackets. An empty host (`file:///x`, a bare path)
// reports false.
func urlHost(raw string) (string, bool) {
	rest := raw
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		rest = rest[i+1:]
	}
	if strings.HasPrefix(rest, "[") {
		j := strings.IndexByte(rest, ']')
		if j < 0 {
			return "", false
		}
		rest = rest[1:j]
	} else if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	rest = strings.ToLower(rest)
	return rest, rest != ""
}
