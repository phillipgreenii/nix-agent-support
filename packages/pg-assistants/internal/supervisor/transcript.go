package supervisor

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"
)

// TranscriptState is what the supervisor needs of a session's transcript.
type TranscriptState struct {
	ModTime   time.Time // the transcript file's last write
	TurnEnded bool      // the last message is an assistant message with no tool call pending
}

// Transcript reads a session's transcript. A session without a transcript
// (yet) is an error wrapping fs.ErrNotExist; the supervisor treats any error
// as "not idle".
type Transcript interface {
	State(cwd, sessionID string) (TranscriptState, error)
}

// FSTranscript reads transcripts under ConfigDir/projects, where Claude keeps
// <sessionID>.jsonl in a directory named for the session's cwd.
type FSTranscript struct{ ConfigDir string }

// tailBytes is how much of the transcript's end State reads; the last message
// is far shorter, except one with a huge tool result, which is a user message
// that ends the search anyway.
const tailBytes = 256 << 10

// State implements Transcript.
func (f FSTranscript) State(cwd, sessionID string) (TranscriptState, error) {
	path := filepath.Join(f.ConfigDir, "projects", projectDir(cwd), sessionID+".jsonl")
	file, err := os.Open(path)
	if err != nil {
		return TranscriptState{}, err
	}
	defer func() { _ = file.Close() }()
	fi, err := file.Stat()
	if err != nil {
		return TranscriptState{}, err
	}
	off := max(fi.Size()-tailBytes, 0)
	data, err := io.ReadAll(io.NewSectionReader(file, off, fi.Size()-off))
	if err != nil {
		return TranscriptState{}, err
	}
	if off > 0 { // the first line is cut
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return TranscriptState{ModTime: fi.ModTime(), TurnEnded: lastTurnEnded(data)}, nil
}

// projectDir is Claude's directory name for a cwd: every character that is
// not a letter, digit or "-" becomes "-".
func projectDir(cwd string) string {
	b := []byte(cwd)
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
			b[i] = '-'
		}
	}
	return string(b)
}

type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// lastTurnEnded reports whether the last user or assistant line of the
// transcript data is an assistant message holding no tool call. A tool call
// (a long Bash, a subagent) leaves the last message an assistant tool_use, so
// a session running one is never reported as ended.
func lastTurnEnded(data []byte) bool {
	lines := bytes.Split(data, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		var l transcriptLine
		if json.Unmarshal(lines[i], &l) != nil || (l.Type != "assistant" && l.Type != "user") {
			continue
		}
		return l.Type == "assistant" && !bytes.Contains(l.Message.Content, []byte(`"tool_use"`))
	}
	return false
}
