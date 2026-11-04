package copier

import "github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"

// SetOnFileChange registers fn to be told, after each commit, of every file
// whose persisted state the commit changed, with the file's copy offset: the end
// of the commit when the commit moved it, else the file's current recorded copy
// offset (a tombstone or a path change moves none). fn runs on the committing
// goroutine and MUST NOT block. Nil clears it.
func (c *Copier) SetOnFileChange(fn func(file, copyOffset int64)) {
	if fn == nil {
		c.onChange.Store(nil)
		return
	}
	c.onChange.Store(&fn)
}

// commit commits recs and then reports the files they changed.
func (c *Copier) commit(recs ...*store.Rec) error {
	if err := c.st.Commit(recs...); err != nil {
		return err
	}
	if c.onChange.Load() == nil {
		return nil
	}
	moved := map[int64]int64{}
	var order []int64
	for _, r := range recs {
		if r.File == 0 {
			continue
		}
		if _, ok := moved[r.File]; !ok {
			moved[r.File] = -1
			order = append(order, r.File)
		}
		if r.T == store.TCommit {
			moved[r.File] = r.To
		}
	}
	for _, id := range order {
		c.noteOffset(id, moved[id])
	}
	return nil
}

// noteFiles reports files whose state changed outside a commit this package
// issued itself (their offsets are read from the store).
func (c *Copier) noteFiles(ids ...int64) {
	for _, id := range ids {
		c.noteOffset(id, -1)
	}
}

// noteOffset tells the hook about a file; off < 0 means read the file's current
// recorded copy offset.
func (c *Copier) noteOffset(id, off int64) {
	fn := c.onChange.Load()
	if fn == nil {
		return
	}
	if off < 0 {
		off = c.RecordedOffset(id)
	}
	(*fn)(id, off)
}

// RecordedOffset is the copy offset of the file's current generation (0 when it
// has none, or the file or generation cannot be read).
func (c *Copier) RecordedOffset(id int64) int64 {
	f, err := c.st.File(id)
	if err != nil || f.CurGen <= 0 {
		return 0
	}
	g, err := c.st.Generation(id, f.CurGen)
	if err != nil {
		return 0
	}
	return g.CopyOffset
}
