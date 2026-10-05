package workitem

import (
	"encoding/json"
	"sort"
)

// KeyListContract is the version string of the exported key-list file
// (share/work-item-keys.json).
const KeyListContract = "pg-decider.work-item-keys/v1"

// KeyListFile is the shape of share/work-item-keys.json: a version string and,
// per kind, the sorted field names a role may rely on. Phase 9's
// deployment-repo check reads this file to verify that role prompts mention
// only fields in the work-item contract.
type KeyListFile struct {
	Contract string              `json:"contract"`
	Kinds    map[string][]string `json:"kinds"`
}

// KeyList derives the per-kind field list from the contract table; it is not
// maintained separately. For each kind the list holds the structural fields
// "issue_type" and "title" always, "labels" when the kind has labels,
// "parent" when the kind has a parent, and every metadata key (including
// dedup_key) verbatim. The list is sorted and free of duplicates.
func KeyList() KeyListFile {
	out := KeyListFile{Contract: KeyListContract, Kinds: map[string][]string{}}
	for _, k := range Kinds() {
		c := ContractFor(k)
		set := map[string]bool{"issue_type": true, "title": true}
		if len(c.Labels) > 0 {
			set["labels"] = true
		}
		if c.Parent != "" {
			set["parent"] = true
		}
		for _, mk := range c.MetadataKeys {
			set[mk] = true
		}
		keys := make([]string, 0, len(set))
		for key := range set {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out.Kinds[string(k)] = keys
	}
	return out
}

// MarshalKeyList renders KeyList as the exact bytes of the golden file:
// two-space-indented JSON, kinds in encoding/json's sorted key order, and a
// trailing newline.
func MarshalKeyList() ([]byte, error) {
	b, err := json.MarshalIndent(KeyList(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
