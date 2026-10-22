package bgkill

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// FileStore reads bgrun's own job records (NAME.meta, NAME.pid, NAME.exit in the
// state directory; packages/bg-tools/lib/bg-tools-lib.bash is the format's
// definition) and probes the recorded pid, so the rule decides on the same
// evidence bgkill itself re-checks.
type FileStore struct {
	// Dir is bgrun's state directory.
	Dir string
}

// DefaultDir mirrors bg_state_dir: $BG_DIR, else $TMPDIR/pg-bg-$USER. The hook
// process resolves it from ITS environment, which may differ from the Bash
// tool's; a record the hook cannot see is simply not approved.
func DefaultDir() string {
	if d := os.Getenv("BG_DIR"); d != "" {
		return d
	}
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	user := os.Getenv("USER")
	if user == "" {
		user = currentUserName()
	}
	return filepath.Join(strings.TrimRight(tmp, "/"), "pg-bg-"+user)
}

func currentUserName() string {
	out, err := exec.Command("id", "-un").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// NewFileStore returns a FileStore over DefaultDir().
func NewFileStore() *FileStore { return &FileStore{Dir: DefaultDir()} }

// Job implements JobStore. A name with no pid record is not found.
func (s *FileStore) Job(name string) (Job, bool) {
	if !nameRe.MatchString(name) {
		return Job{}, false
	}
	pidBytes, err := os.ReadFile(filepath.Join(s.Dir, name+".pid"))
	if err != nil {
		return Job{}, false
	}
	var job Job
	if _, err := os.Stat(filepath.Join(s.Dir, name+".exit")); err == nil {
		job.Finished = true
	}
	meta := readMeta(filepath.Join(s.Dir, name+".meta"))
	job.Session = meta["session"]
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil || pid <= 1 {
		return job, true
	}
	job.LauncherAlive = launcherAlive(pid, meta["pidstart"])
	return job, true
}

// readMeta returns the FIRST value of each `key: value` line (bgrun writes
// `session:` first, so a later free-text line cannot shadow it).
func readMeta(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ": ")
		if !ok {
			continue
		}
		if _, seen := out[k]; !seen {
			out[k] = v
		}
	}
	return out
}

// launcherAlive reports whether pid is a live process that is still the bgrun
// launcher: signal 0 succeeds, its command line names bgrun, and its start time
// equals the recorded one when bgrun recorded one.
func launcherAlive(pid int, recordedStart string) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false // gone, or not ours (EPERM): never approve either
	}
	cmdline, err := psField(pid, "command=")
	if err != nil || !strings.Contains(cmdline, "bgrun") {
		return false
	}
	if recordedStart == "" {
		return true
	}
	start, err := psField(pid, "lstart=")
	return err == nil && start == recordedStart
}

func psField(pid int, field string) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", field).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
