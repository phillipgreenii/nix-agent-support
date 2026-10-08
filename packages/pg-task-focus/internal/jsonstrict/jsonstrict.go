// Package jsonstrict closes a hole in encoding/json: a JSON object that
// repeats a key decodes without complaint, the last value winning. A log line
// that says "v":2 and then "v":1 must not be read as version 1, and a task
// defined twice in a configuration must not silently lose its first
// definition, so the documents of this module are checked for repeated keys
// before they are decoded.
package jsonstrict

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DuplicateKeyError reports a key that an object writes more than once.
type DuplicateKeyError struct {
	// Key is the repeated key as encoding/json would decode it, unescaped: a
	// key spelled with a unicode escape is the same key as its plain spelling.
	Key string
	// Pointer is the JSON pointer (RFC 6901) of the object that holds the
	// key, empty for the document's own object.
	Pointer string
}

func (e *DuplicateKeyError) Error() string {
	where := e.Pointer
	if where == "" {
		where = "/"
	}
	return fmt.Sprintf("duplicate key %q at %s", e.Key, where)
}

// frame is one open object or array of the walk.
type frame struct {
	// label is how the container is reached from its parent: the parent's
	// current key, or the array index. Pointers are built from labels only
	// when an error needs one, so a deeply nested document costs no more
	// than its depth.
	label  string
	object bool
	keys   map[string]struct{} // the keys seen so far, for an object
	key    string              // the key whose value is being read, for an object
	next   int                 // the index of the next item, for an array
	want   bool                // an object is waiting for its next key
}

// CheckNoDuplicateKeys reports the first key that an object of data, at any
// depth, writes twice. Keys are compared as encoding/json would, after
// unescaping, so two spellings of one key are one key; a key at one level
// never clashes with the same key at another. It returns a *DuplicateKeyError
// for such a key. data MUST be exactly one JSON value: anything else is
// reported as the syntax error it is. The walk is iterative, so the nesting
// depth of data is not limited by the stack.
func CheckNoDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var stack []frame
	done := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if !done {
				return io.ErrUnexpectedEOF
			}
			return nil
		}
		if err != nil {
			return err
		}
		if done {
			return errors.New("there is data after the JSON value")
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].want {
			top := &stack[n-1]
			switch t := tok.(type) {
			case string:
				if _, seen := top.keys[t]; seen {
					return &DuplicateKeyError{Key: t, Pointer: pointer(stack)}
				}
				top.keys[t] = struct{}{}
				top.key, top.want = t, false
			case json.Delim: // the '}' that closes the object
				stack = stack[:n-1]
				done = finish(stack)
			}
			continue
		}
		// A value: a scalar, or the opening or closing delimiter of one.
		d, isDelim := tok.(json.Delim)
		switch {
		case isDelim && (d == '{' || d == '['):
			f := frame{label: label(stack), object: d == '{'}
			if f.object {
				f.keys, f.want = map[string]struct{}{}, true
			}
			stack = append(stack, f)
		case isDelim: // the ']' that closes an array
			stack = stack[:len(stack)-1]
			done = finish(stack)
		default:
			done = finish(stack)
		}
	}
}

// label is how a value that starts now is reached from the open container.
func label(stack []frame) string {
	if len(stack) == 0 {
		return ""
	}
	top := stack[len(stack)-1]
	if top.object {
		return top.key
	}
	return strconv.Itoa(top.next)
}

// finish records that the value that was being read is complete, and reports
// whether that was the document itself.
func finish(stack []frame) bool {
	if len(stack) == 0 {
		return true
	}
	top := &stack[len(stack)-1]
	if top.object {
		top.want = true
	} else {
		top.next++
	}
	return false
}

// pointer is the JSON pointer of the innermost open object.
func pointer(stack []frame) string {
	var sb strings.Builder
	for _, f := range stack[1:] {
		sb.WriteByte('/')
		sb.WriteString(strings.NewReplacer("~", "~0", "/", "~1").Replace(f.label))
	}
	return sb.String()
}
