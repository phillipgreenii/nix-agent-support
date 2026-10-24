package prchange

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The delivery key (design 6.1, "Attribution (risk R8)"): the number the command
// writes into `metadata.seq` of the minimal routed item it hands the decider's
// `--from-item -`, so the decider's `escalate_after` counter counts one
// delivery once. A run without `--from-item` always counts, so a router retry
// of one delivery would otherwise raise a premature human task.
//
// The key is, in order: (1) the change sequence the item carries (the daemon
// feed does), (2) the integer value of the 12 hexadecimal digits after the @ of
// the item id (the retry-window id every item of the old backend carries; the
// suffix is a digest of the change kind, source, entity id, head and version,
// so a router retry of one delivery carries the same suffix), (3) none: no
// --from-item is passed, because no retry window means no router retry, so
// counting every run is correct. A key of 0 is never used: the decider treats
// an item as carrying a sequence whatever its value, and a pair of equal keys
// makes it skip the count.

// Where a delivery key came from.
const (
	KeySourceSequence = "sequence"  // the change sequence argument
	KeySourceSuffix   = "id-suffix" // the 12 hex digits after @ in the item id
	KeySourceNone     = "none"      // no key: no --from-item
)

// DeliveryKey is the resolved key of one run. Value is only meaningful when
// Source is not KeySourceNone, and is never 0 then.
type DeliveryKey struct {
	Value  int64
	Source string
}

// Has reports whether the run has a key and so passes --from-item.
func (k DeliveryKey) Has() bool { return k.Source != KeySourceNone && k.Value != 0 }

// idSuffixRe captures the digits of the retry-window suffix.
var idSuffixRe = regexp.MustCompile(`@([0-9a-fA-F]{12})$`)

// absentSeqTexts are the argument texts that mean "the item has no sequence":
// the empty text, and what a templating engine renders for a missing key or a
// null (`<no value>`, `<nil>`, `null`).
var absentSeqTexts = map[string]bool{"": true, "<no value>": true, "<nil>": true, "null": true}

// ParseSeq reads the change sequence argument. ok is false when the argument
// names no usable sequence: it is absent (see absentSeqTexts) or is 0. A
// sequence the router rendered as a JSON number may be in exponent form (a
// sequence of 1,000,000 or more renders as for example 1.234567e+06, because
// the handler decodes the item payload into untyped values), so the plain
// integer form is read exactly and every other form as a float that must be a
// whole, positive number inside int64. Text that is none of these is an error:
// a template that renders garbage is a deployment defect, reported rather than
// read as "no sequence".
func ParseSeq(arg string) (n int64, ok bool, err error) {
	s := strings.TrimSpace(arg)
	if absentSeqTexts[strings.ToLower(s)] {
		return 0, false, nil
	}
	if v, perr := strconv.ParseInt(s, 10, 64); perr == nil {
		return positiveOrAbsent(v)
	}
	f, perr := strconv.ParseFloat(s, 64)
	if perr != nil || math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < 0 || f >= math.MaxInt64 {
		return 0, false, fmt.Errorf("the sequence %q is not a whole, non-negative number (plain or in exponent form)", arg)
	}
	return positiveOrAbsent(int64(f))
}

func positiveOrAbsent(v int64) (int64, bool, error) {
	if v < 0 {
		return 0, false, fmt.Errorf("the sequence %d is negative", v)
	}
	if v == 0 {
		return 0, false, nil
	}
	return v, true, nil
}

// ResolveKey picks the delivery key of a run from the sequence argument and the
// item id as the router gave it.
func ResolveKey(seqArg, rawID string) (DeliveryKey, error) {
	n, ok, err := ParseSeq(seqArg)
	if err != nil {
		return DeliveryKey{Source: KeySourceNone}, err
	}
	if ok {
		return DeliveryKey{Value: n, Source: KeySourceSequence}, nil
	}
	if m := idSuffixRe.FindStringSubmatch(strings.TrimSpace(rawID)); m != nil {
		if v, perr := strconv.ParseUint(m[1], 16, 64); perr == nil && v != 0 && v <= math.MaxInt64 {
			return DeliveryKey{Value: int64(v), Source: KeySourceSuffix}, nil
		}
	}
	return DeliveryKey{Source: KeySourceNone}, nil
}

// routedItem is the minimal routed pg-router item the decider's item.Routed
// reads: id, type, title and metadata with entity_type, entity_id, kind and seq.
type routedItem struct {
	ID       string     `json:"id"`
	Type     string     `json:"type"`
	Title    string     `json:"title"`
	Metadata routedMeta `json:"metadata"`
}

type routedMeta struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Kind       string `json:"kind"`
	Seq        int64  `json:"seq"`
}

// RoutedItemJSON builds the item the command hands the decider on standard
// input. The kind is `changed`: the decider never decides from it, and apply
// reads only the item's identity and sequence.
func RoutedItemJSON(rawID, entityID string, key DeliveryKey) string {
	b, err := json.Marshal(routedItem{
		ID: strings.TrimSpace(rawID), Type: "pr.changed", Title: entityID + " changed",
		Metadata: routedMeta{EntityType: "pr", EntityID: entityID, Kind: "changed", Seq: key.Value},
	})
	if err != nil {
		// Unreachable: the struct holds only strings and an integer.
		panic(err)
	}
	return string(b) + "\n"
}
