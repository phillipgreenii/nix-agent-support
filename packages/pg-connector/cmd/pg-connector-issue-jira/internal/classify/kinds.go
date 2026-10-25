package classify

// The Jira change kinds, spelled exactly as the design spells them.
const (
	KindClosed             = "closed"
	KindReopened           = "reopened"
	KindStatusChanged      = "status_changed"
	KindAssigneeChanged    = "assignee_changed"
	KindPriorityChanged    = "priority_changed"
	KindLabelsChanged      = "labels_changed"
	KindParentChanged      = "parent_changed"
	KindTitleChanged       = "title_changed"
	KindDescriptionChanged = "description_changed"
	KindDepsChanged        = "deps_changed"
	KindCommentsChanged    = "comments_changed"
	KindRenamed            = "renamed"
	KindRemoved            = "removed"
	KindFieldsChanged      = "fields_changed"

	// KindEnteredQuery and KindLeftQuery are membership information, never the
	// kind of a stored row.
	KindEnteredQuery = "entered_query"
	KindLeftQuery    = "left_query"

	// KindAdded is the one first-observation kind. It is not produced by a
	// classification here and is not listed by Kinds; the change-engine
	// registration appends it to the canonical list.
	KindAdded = "added"
)

// Kinds returns the kind names in the canonical order every kind list uses:
// closed, reopened, status_changed, assignee_changed, priority_changed,
// labels_changed, parent_changed, title_changed, description_changed,
// deps_changed, comments_changed, renamed, removed, fields_changed,
// entered_query, left_query. The caller owns the slice.
func Kinds() []string {
	return []string{
		KindClosed, KindReopened, KindStatusChanged, KindAssigneeChanged,
		KindPriorityChanged, KindLabelsChanged, KindParentChanged, KindTitleChanged,
		KindDescriptionChanged, KindDepsChanged, KindCommentsChanged, KindRenamed,
		KindRemoved, KindFieldsChanged, KindEnteredQuery, KindLeftQuery,
	}
}
