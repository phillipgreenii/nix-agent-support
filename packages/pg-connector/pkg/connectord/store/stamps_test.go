package store

import (
	"context"
	"testing"
)

// The stamp accessors confine the shipped stamp columns (head_sha, base_sha,
// fetched_for_head, fetched_for_base): code outside the store reads and writes
// a row's stamps through them. This test round trips each accessor through the
// real store.
func TestStampAccessorsRoundTripTheStampColumns(t *testing.T) {
	ctx := context.Background()
	s, _ := mustOpen(t, dbPath(t), fastOpts())

	var c ChangeRow
	c.Kind, c.ID, c.Version, c.At = "example-kind", "example-1", 1, ms(1)
	c.SetStamp("stamp-a")
	if c.Stamp() != "stamp-a" {
		t.Fatalf("ChangeRow.Stamp = %q, want stamp-a", c.Stamp())
	}
	seq := appendRow(t, s, c)
	rows, err := s.ChangesAfter(ctx, seq-1, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ChangesAfter = %v, %v", rows, err)
	}
	if rows[0].Stamp() != "stamp-a" {
		t.Errorf("stored ChangeRow.Stamp = %q, want stamp-a", rows[0].Stamp())
	}

	var p PendingChange
	p.Kind, p.ID = "example-kind", "example-2"
	p.SetStamps("stamp-1", "stamp-2")
	if a, b := p.Stamps(); a != "stamp-1" || b != "stamp-2" {
		t.Errorf("PendingChange.Stamps = %q, %q", a, b)
	}
	if err := s.InTx(ctx, func(tx *Tx) error { return tx.PutPending(ctx, p) }); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListPending(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListPending = %v, %v", pending, err)
	}
	if a, b := pending[0].Stamps(); a != "stamp-1" || b != "stamp-2" {
		t.Errorf("stored PendingChange.Stamps = %q, %q", a, b)
	}

	var g GroupRow
	g.Kind, g.ID, g.Group, g.Content = "example-kind", "example-3", "example-group", []byte("x")
	g.SetStamps("fetched-1", "fetched-2")
	if err := s.PutGroup(ctx, g); err != nil {
		t.Fatal(err)
	}
	back, ok, err := s.GetGroup(ctx, "example-kind", "example-3", "example-group")
	if err != nil || !ok {
		t.Fatalf("GetGroup = %v, %v", ok, err)
	}
	if a, b := back.Stamps(); a != "fetched-1" || b != "fetched-2" {
		t.Errorf("stored GroupRow.Stamps = %q, %q", a, b)
	}
}
