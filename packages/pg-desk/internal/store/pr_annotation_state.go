package store

import (
	"encoding/json"
	"fmt"
)

// PRHiddenWIP returns an entity's hidden flag, hidden reason and wip flag,
// whichever annotation shape the store is on: the old per-column PR-level row
// on version 1, the hidden and wip key/value annotations on version 2. An
// entity with no annotation is not hidden and not wip. Callers that only
// need these three facts use it instead of reading either shape directly.
func (s *Store) PRHiddenWIP(repo, entityType, entityID string) (hidden bool, reason string, wip bool, err error) {
	newSchema, err := s.isNewSchema()
	if err != nil {
		return false, "", false, err
	}
	if !newSchema {
		ann, found, err := s.GetPRAnnotation(repo, entityType, entityID)
		if err != nil || !found {
			return false, "", false, err
		}
		if ann.Hidden != nil {
			hidden = *ann.Hidden
		}
		if ann.WIP != nil {
			wip = *ann.WIP
		}
		return hidden, ann.HiddenReason, wip, nil
	}

	h, found, err := s.GetKVAnnotation(repo, entityType, entityID, AnnotationHidden)
	if err != nil {
		return false, "", false, err
	}
	if found {
		var v struct {
			Value  bool    `json:"value"`
			Reason *string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(h.Value), &v); err != nil {
			return false, "", false, fmt.Errorf("store: decode hidden annotation (%s,%s,%s): %w", repo, entityType, entityID, err)
		}
		hidden = v.Value
		if v.Reason != nil {
			reason = *v.Reason
		}
	}
	w, found, err := s.GetKVAnnotation(repo, entityType, entityID, AnnotationWIP)
	if err != nil {
		return false, "", false, err
	}
	return hidden, reason, found && w.Value == "true", nil
}
