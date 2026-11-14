package settle

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/store"
)

// changedAgainst is pure over a settle record, a Conversation and an offset
// reader, so every case here is a table row with no store.
func TestChangedAgainstTable(t *testing.T) {
	i64 := func(n int64) *int64 { return &n }
	mf := func(id int64, mode string, gen int, size int64, last *int64) store.ManifestFile {
		return store.ManifestFile{File: id, Mode: mode, Gen: gen, Size: size, LastSize: last}
	}
	file := func(id int64, mode string, gen int, last int64) store.File {
		return store.File{ID: id, Mode: mode, CurGen: gen, LastSize: last}
	}
	// offsets answers the copy offset of a file's current generation.
	offsets := func(m map[int64]int64) func(store.File) (int64, bool) {
		return func(f store.File) (int64, bool) {
			v, ok := m[f.ID]
			return v, ok
		}
	}
	none := offsets(nil)

	tests := []struct {
		name     string
		manifest []store.ManifestFile
		raw      string // when set, the manifest bytes instead of manifest
		files    []store.File
		offset   func(store.File) (int64, bool)
		want     bool
	}{
		{name: "no files and an empty manifest is unchanged", offset: none, want: false},
		{name: "unreadable manifest reads as changed", raw: "{not json", offset: none, want: true},
		{
			name:     "a file the manifest does not list is a change",
			manifest: []store.ManifestFile{mf(1, "append", 1, 10, nil)},
			files:    []store.File{file(1, "append", 1, 0), file(2, "append", 1, 0)},
			offset:   offsets(map[int64]int64{1: 10, 2: 5}),
			want:     true,
		},
		{
			name:     "append file, same generation and offset, is unchanged",
			manifest: []store.ManifestFile{mf(1, "append", 2, 10, nil)},
			files:    []store.File{file(1, "append", 2, 0)},
			offset:   offsets(map[int64]int64{1: 10}),
			want:     false,
		},
		{
			name:     "append file, new generation is a change",
			manifest: []store.ManifestFile{mf(1, "append", 2, 10, nil)},
			files:    []store.File{file(1, "append", 3, 0)},
			offset:   offsets(map[int64]int64{1: 10}),
			want:     true,
		},
		{
			name:     "append file, more bytes copied is a change",
			manifest: []store.ManifestFile{mf(1, "append", 2, 10, nil)},
			files:    []store.File{file(1, "append", 2, 0)},
			offset:   offsets(map[int64]int64{1: 11}),
			want:     true,
		},
		{
			name:     "append file whose offset cannot be read answers unchanged",
			manifest: []store.ManifestFile{mf(1, "append", 2, 10, nil)},
			files:    []store.File{file(1, "append", 2, 0)},
			offset:   none,
			want:     false,
		},
		{
			name:     "append file with no generation reads offset 0 and matches a zero size",
			manifest: []store.ManifestFile{mf(1, "append", 1, 0, nil)},
			files:    []store.File{file(1, "append", 1, 0)},
			offset:   offsets(map[int64]int64{1: 0}),
			want:     false,
		},
		{
			name:     "snapshot, row size equal to the recorded last size is unchanged",
			manifest: []store.ManifestFile{mf(1, "snapshot", 3, 22, i64(1022927))},
			files:    []store.File{file(1, "snapshot", 3, 1022927)},
			offset:   none,
			want:     false,
		},
		{
			name:     "snapshot with no last size falls back to the stored size",
			manifest: []store.ManifestFile{mf(1, "snapshot", 3, 22, nil)},
			files:    []store.File{file(1, "snapshot", 3, 22)},
			offset:   none,
			want:     false,
		},
		{
			name:     "snapshot, row size differs is a change",
			manifest: []store.ManifestFile{mf(1, "snapshot", 3, 22, i64(100))},
			files:    []store.File{file(1, "snapshot", 3, 101)},
			offset:   none,
			want:     true,
		},
		{
			name:     "snapshot, new generation is a change",
			manifest: []store.ManifestFile{mf(1, "snapshot", 3, 22, nil)},
			files:    []store.File{file(1, "snapshot", 4, 22)},
			offset:   none,
			want:     true,
		},
		{
			name:     "snapshot never consults the offset reader",
			manifest: []store.ManifestFile{mf(1, "snapshot", 3, 22, nil)},
			files:    []store.File{file(1, "snapshot", 3, 22)},
			offset:   func(store.File) (int64, bool) { panic("offset read for a snapshot file") },
			want:     false,
		},
		{
			name:     "a later change is found after an unchanged file",
			manifest: []store.ManifestFile{mf(1, "append", 1, 5, nil), mf(2, "append", 1, 7, nil)},
			files:    []store.File{file(1, "append", 1, 0), file(2, "append", 1, 0)},
			offset:   offsets(map[int64]int64{1: 5, 2: 8}),
			want:     true,
		},
		{
			name:     "an unreadable offset ends the scan as unchanged even if a later file changed",
			manifest: []store.ManifestFile{mf(1, "append", 1, 5, nil), mf(2, "append", 1, 7, nil)},
			files:    []store.File{file(1, "append", 1, 0), file(2, "append", 1, 0)},
			offset:   offsets(map[int64]int64{2: 8}),
			want:     false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.raw)
			if tc.raw == "" {
				var err error
				raw, err = json.Marshal(store.Manifest{Files: tc.manifest})
				if err != nil {
					t.Fatal(err)
				}
			}
			c := &conv{session: "s", files: tc.files}
			if got := changedAgainst(store.Settled{Manifest: raw}, c, tc.offset); got != tc.want {
				t.Fatalf("changedAgainst = %v, want %v", got, tc.want)
			}
		})
	}
}
