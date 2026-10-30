//go:build !linux && !(darwin && cgo)

package watcher

func newBackend() backend { return nil }
