// discovery.go: enumeration of every transcript file under a Claude Code
// projects tree, without opening any of them. Unlike Sessions it also reports
// the transcripts of subagents, and it reports directory read failures instead
// of hiding them, because a caller that must prove "no transcript names X"
// cannot treat an unreadable directory as an empty one.
package claudetranscript

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// subagentsDirName is the directory next to a session's transcript that holds
// the transcripts of the subagents the session spawned.
const subagentsDirName = "subagents"

// TranscriptFile is one transcript found under a projects tree.
type TranscriptFile struct {
	// Path is the file's path, rooted at the projects directory passed to
	// DiscoverTranscripts.
	Path string
	// Subagent is true for <slug>/<session>/subagents/<file>.jsonl and false
	// for a session's own <slug>/<session>.jsonl.
	Subagent bool
}

// DiscoverTranscripts lists the transcripts under projectsDir sorted
// by path: each <slug>/<session>.jsonl, and each
// <slug>/<session>/subagents/<file>.jsonl. A <file>.status.jsonl statusline
// sidecar is never a transcript. No file is opened.
//
// A directory that vanishes during the walk is skipped (a session being
// cleaned up is not a failure). Any other directory read failure, including an
// unreadable projectsDir, is joined into the returned error; the files that
// could be listed are still returned.
func DiscoverTranscripts(projectsDir string) ([]TranscriptFile, error) {
	slugs, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, err
	}
	var (
		files []TranscriptFile
		errs  []error
	)
	readDir := func(dir string) []fs.DirEntry {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
			return nil
		}
		return entries
	}
	for _, slug := range slugs {
		if !slug.IsDir() {
			continue
		}
		slugDir := filepath.Join(projectsDir, slug.Name())
		for _, e := range readDir(slugDir) {
			if !e.IsDir() {
				if isSessionTranscriptName(e.Name()) {
					files = append(files, TranscriptFile{Path: filepath.Join(slugDir, e.Name())})
				}
				continue
			}
			subDir := filepath.Join(slugDir, e.Name(), subagentsDirName)
			for _, s := range readDir(subDir) {
				if !s.IsDir() && isSessionTranscriptName(s.Name()) {
					files = append(files, TranscriptFile{Path: filepath.Join(subDir, s.Name()), Subagent: true})
				}
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, errors.Join(errs...)
}
