//go:build !darwin

package fsx

import "os"

func fullSyncFile(f *os.File) error { return f.Sync() }
