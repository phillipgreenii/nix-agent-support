package fsx

import (
	"os"
	"path/filepath"
)

// FullSyncer is a file that can flush the drive's cache (F_FULLFSYNC on darwin;
// a plain fsync elsewhere, where fsync already means the drive).
type FullSyncer interface{ FullSync() error }

// FullSync flushes f as hard as the platform allows: through FullSync when f
// offers it, through F_FULLFSYNC on a real file on darwin, else a plain fsync.
func FullSync(f File) error {
	if fs, ok := f.(FullSyncer); ok {
		return fs.FullSync()
	}
	if osf, ok := f.(*os.File); ok {
		return fullSyncFile(osf)
	}
	return f.Sync()
}

// DirFullSyncer is an FS that can flush a directory entry as hard as FullSync.
type DirFullSyncer interface{ FullSyncDir(dir string) error }

// FullSyncDir flushes the directory dir (F_FULLFSYNC where the FS supports it).
func FullSyncDir(fsys FS, dir string) error {
	if d, ok := fsys.(DirFullSyncer); ok {
		return d.FullSyncDir(dir)
	}
	return fsys.SyncDir(dir)
}

// Marker is an FS that records named non-file events (the recording wrapper).
type Marker interface{ Mark(op string) }

// Mark records a named event on fsys when it records events; otherwise nothing.
func Mark(fsys FS, op string) {
	if m, ok := fsys.(Marker); ok {
		m.Mark(op)
	}
}

// FullSyncDir flushes a directory with F_FULLFSYNC where the platform has it.
func (OS) FullSyncDir(dir string) error {
	d, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return fullSyncFile(d)
}
