package settle

import "bytes"

// missingBytesRepairable reports whether a manifest file whose stored bytes
// failed to open has a repair or a non-critical state at all: only a snapshot
// file whose bytes are absent (not found) does; every other failure stays a
// plain mismatch.
func missingBytesRepairable(notFound bool, mode string) bool {
	return notFound && mode == "snapshot"
}

// sourceMatchesManifest reports whether the source bytes have the size and hash
// the manifest records, so they may be stored again at the manifest's version.
func sourceMatchesManifest(data []byte, size int64, hash string) bool {
	h, _ := hashReader(bytes.NewReader(data), int64(len(data)))
	return int64(len(data)) == size && hexHash(h) == hash
}
