// Package archive persists the full content of a pending review BEFORE the
// guarded supersede deletes it, so the deletion is recoverable without the
// GitHub API (entity-change-flow contract 9.1, archive location).
//
// Ownership: the location is this backend's own, resolved from the
// environment -- NOT from pg-connector's config -- mirroring the event log
// (package eventlog): the backend is a one-shot process with no access to the
// umbrella's config. It is write-only recovery data, never read back by the
// backend, so it does not make the backend stateful (statelessness, D3).
//
// Failure policy: the opposite of the event log. The write is a precondition
// of the delete, so any failure is reported to the caller, which MUST then not
// delete. Disabling the archive is not possible: with no resolvable location
// every supersede is refused as archive_failed (fail-closed).
package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ServiceName is the directory under the state home that holds the archive.
const ServiceName = "pg-connector-pr-github"

// DirName is the archive directory's name inside the service directory.
const DirName = "archive"

// EnvDir overrides the archive root (used by tests and for relocating it).
const EnvDir = "PG_CONNECTOR_PR_GITHUB_ARCHIVE_DIR"

// RecordVersion is the version of the on-disk record shape.
const RecordVersion = 1

// Comment is one archived inline comment.
type Comment struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Body   string `json:"body"`
	Marked bool   `json:"marked"`
}

// Record is the full content of one pending review, as read from the host.
type Record struct {
	Version     int       `json:"version"`
	ArchivedAt  string    `json:"archived_at"`
	Repo        string    `json:"repo"`
	PR          int       `json:"pr"`
	ReviewID    string    `json:"review_id"`
	DatabaseID  int64     `json:"database_id"`
	URL         string    `json:"url,omitempty"`
	CommitSHA   string    `json:"commit_sha"`
	HeadSHA     string    `json:"head_sha"`
	DigestState string    `json:"digest_state"`
	Body        string    `json:"body"`
	Comments    []Comment `json:"comments"`
}

// Archiver persists a Record and returns where it went.
type Archiver interface {
	Write(rec Record) (path string, err error)
}

// DirArchiver writes one JSON file per review under Dir:
//
//	<Dir>/<owner>/<repo>/pr-<number>/review-<database id>.json
//
// keyed by repo, PR and review id. The file is written atomically (temp file,
// fsync, rename) with mode 0600, then read back and decoded; Write returns
// success only when the read-back matches. Re-archiving the same review
// replaces the file.
type DirArchiver struct {
	Dir string
	Now func() time.Time
}

// DirFromEnv resolves the archive root: EnvDir if set, else
// $XDG_STATE_HOME/pg-connector-pr-github/archive, else
// $HOME/.local/state/pg-connector-pr-github/archive. It returns "" when no
// home can be determined.
func DirFromEnv(getenv func(string) string) string {
	if d := getenv(EnvDir); d != "" {
		return d
	}
	if state := getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, ServiceName, DirName)
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", ServiceName, DirName)
	}
	return ""
}

// FromEnv builds the production archiver. With no resolvable location it still
// returns an archiver, whose Write fails, so the supersede is refused rather
// than silently proceeding without an archive.
func FromEnv(getenv func(string) string) Archiver {
	return DirArchiver{Dir: DirFromEnv(getenv)}
}

// pathPart is the only spelling accepted for a repo owner, repo name: it keeps
// a hostile or malformed id from escaping Dir.
var pathPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Write implements Archiver.
func (a DirArchiver) Write(rec Record) (string, error) {
	if a.Dir == "" {
		return "", errors.New("archive: no archive directory could be determined (set " + EnvDir + ")")
	}
	owner, name, ok := splitRepo(rec.Repo)
	if !ok {
		return "", fmt.Errorf("archive: repo %q is not a safe owner/name", rec.Repo)
	}
	if rec.PR <= 0 || rec.DatabaseID <= 0 {
		return "", fmt.Errorf("archive: PR %d / review database id %d are not positive", rec.PR, rec.DatabaseID)
	}
	if a.Now != nil {
		rec.ArchivedAt = a.Now().UTC().Format(time.RFC3339)
	} else {
		rec.ArchivedAt = time.Now().UTC().Format(time.RFC3339)
	}
	rec.Version = RecordVersion
	dir := filepath.Join(a.Dir, owner, name, fmt.Sprintf("pr-%d", rec.PR))
	final := filepath.Join(dir, fmt.Sprintf("review-%d.json", rec.DatabaseID))

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("archive: marshal: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("archive: create directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".review-*.tmp")
	if err != nil {
		return "", fmt.Errorf("archive: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("archive: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", fmt.Errorf("archive: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", fmt.Errorf("archive: close: %w", err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		cleanup()
		return "", fmt.Errorf("archive: rename into place: %w", err)
	}
	back, err := os.ReadFile(final)
	if err != nil {
		return "", fmt.Errorf("archive: read back: %w", err)
	}
	var check Record
	if err := json.Unmarshal(back, &check); err != nil {
		return "", fmt.Errorf("archive: read-back does not decode: %w", err)
	}
	if check.ReviewID != rec.ReviewID || check.Body != rec.Body || len(check.Comments) != len(rec.Comments) {
		return "", errors.New("archive: read-back does not match what was written")
	}
	return final, nil
}

// splitRepo splits "owner/name" and checks both parts are safe path segments.
func splitRepo(repo string) (owner, name string, ok bool) {
	for i := 0; i < len(repo); i++ {
		if repo[i] == '/' {
			owner, name = repo[:i], repo[i+1:]
			ok = pathPart.MatchString(owner) && pathPart.MatchString(name) && owner != ".." && name != ".."
			return owner, name, ok
		}
	}
	return "", "", false
}
