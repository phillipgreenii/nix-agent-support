package store

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
)

// DeletedSnapshotFiles returns exactly the snapshot-mode files in state
// deleted, the same rows a filter over AllFiles selects.
func TestDeletedSnapshotFilesReturnsOnlyDeletedSnapshots(t *testing.T) {
	s := open(t, t.TempDir())
	defer func() { _ = s.Close() }()
	rows := []struct {
		id    int64
		mode  string
		state string
	}{
		{1, "append", "ok"},
		{2, "append", "deleted"},
		{3, "snapshot", "ok"},
		{4, "snapshot", "deleted"},
		{5, "snapshot", "deleted"},
		{6, "snapshot", "copy_mismatch"},
	}
	err := s.WithWrite("", func(tx *sql.Tx) error {
		for _, r := range rows {
			if _, err := tx.ExecContext(context.Background(), `INSERT INTO files(id,source,mode,state,first_seen_ns,cur_gen) VALUES(?,?,?,?,1,1)`, r.id, "x", r.mode, r.state); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.DeletedSnapshotFiles()
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, f := range got {
		ids = append(ids, f.ID)
	}
	if want := []int64{4, 5}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("got %v want %v", ids, want)
	}
	all, _ := s.AllFiles()
	var filtered []File
	for _, f := range all {
		if f.Mode == "snapshot" && f.State == "deleted" {
			filtered = append(filtered, f)
		}
	}
	if !reflect.DeepEqual(got, filtered) {
		t.Fatalf("differs from the AllFiles filter: %v vs %v", got, filtered)
	}
}
