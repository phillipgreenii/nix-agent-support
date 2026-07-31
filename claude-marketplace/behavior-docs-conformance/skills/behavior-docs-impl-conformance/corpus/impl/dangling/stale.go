package dangling

// Reject implements INV-404: a rule this set does not define and does not
// declare in its imports table, so the citation resolves to nothing.
func Reject() {}
