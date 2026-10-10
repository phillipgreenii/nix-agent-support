package focus

import (
	"fmt"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-desk/internal/store"
)

// Annotator writes the focus_selected annotation of one entity. It is the
// seam the lock, pull, close and repair paths call once per affected entity,
// after the table transaction has committed.
//
// SetFocusSelected stores value under the focus_selected key. It returns the
// change_log sequence of the annotation_changed record it appended and
// changed=true, or seq=0 and changed=false when the entity already holds
// value (a re-run is idempotent and appends no redundant record). A failure
// is returned as err and leaves the annotation as it was.
type Annotator interface {
	SetFocusSelected(repo, entityType, entityID, value, origin, actor, at string) (seq int64, changed bool, err error)
}

// StoreAnnotator is the production Annotator: it writes through the store
// while holding pg-desk's per-entity lock, so two concurrent select or pull
// runs (two operators, or an operator and an agent) serialize per entity and
// the later run's read sees the earlier run's write.
type StoreAnnotator struct {
	Store  *store.Store
	Locker *store.Locker
}

// NewAnnotator returns the production Annotator over s, serializing per
// entity under l.
func NewAnnotator(s *store.Store, l *store.Locker) *StoreAnnotator {
	return &StoreAnnotator{Store: s, Locker: l}
}

// SetFocusSelected implements Annotator. The lock is held for the whole
// read-then-write, and is released on every path.
func (a *StoreAnnotator) SetFocusSelected(repo, entityType, entityID, value, origin, actor, at string) (int64, bool, error) {
	unlock, err := a.Locker.Lock(repo, entityType, entityID)
	if err != nil {
		return 0, false, fmt.Errorf("focus: annotate (%s,%s,%s): %w", repo, entityType, entityID, err)
	}
	defer unlock()
	return a.Store.SetAnnotationSeq(store.KVAnnotation{
		Repo: repo, EntityType: entityType, EntityID: entityID,
		Key: store.AnnotationFocusSelected, Value: value,
		Origin: origin, SetBy: actor, SetAt: at,
	})
}

var _ Annotator = (*StoreAnnotator)(nil)
