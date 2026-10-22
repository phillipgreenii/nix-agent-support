package docker

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// transcriptBuilder writes a Claude Code transcript JSONL fixture: each call is
// an `assistant` tool_use line followed by its `user` tool_result line, the two
// record shapes loadOwned reads.
type transcriptBuilder struct {
	t     *testing.T
	lines []string
	n     int
}

func (b *transcriptBuilder) call(command, output string, isError bool) *transcriptBuilder {
	b.n++
	id := fmt.Sprintf("toolu_%03d", b.n)
	use := map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]string{"command": command}},
	}}}
	res := map[string]any{"type": "user", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isError, "content": output},
	}}}
	for _, v := range []any{use, res} {
		raw, err := json.Marshal(v)
		if err != nil {
			b.t.Fatal(err)
		}
		b.lines = append(b.lines, string(raw))
	}
	return b
}

func (b *transcriptBuilder) ok(command, output string) *transcriptBuilder {
	return b.call(command, output, false)
}

func (b *transcriptBuilder) write() string {
	b.t.Helper()
	path := filepath.Join(b.t.TempDir(), "transcript.jsonl")
	// A noise line of another type must be ignored, not break parsing.
	body := `{"type":"attachment","message":{"content":"x"}}` + "\n" + strings.Join(b.lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		b.t.Fatal(err)
	}
	return path
}

const fullID = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func ownedRule() *Rule {
	return New(&mockEvaluator{defaultResult: hookio.RuleResult{Decision: hookio.NoOpinion, Module: "mock"}}, nil)
}

func evalDocker(t *testing.T, command, transcript string) (hookio.RuleResult, error) {
	t.Helper()
	input := &hookio.HookInput{
		SessionID:      "sess-1",
		ToolName:       "Bash",
		ToolInput:      mustJSON(map[string]string{"command": command}),
		CWD:            "/tmp/project",
		TranscriptPath: transcript,
	}
	return ownedRule().Evaluate(input)
}

// sessionTranscript is a session that created one of each object kind, once
// labeled the way agent rule C-2 asks (bead and owner labels), once not.
func sessionTranscript(t *testing.T) string {
	b := &transcriptBuilder{t: t}
	b.ok("docker run -d --name mine --label bead=pg2-04xmm --label owner=sess-1 alpine sleep 600", fullID+"\n")
	b.ok("docker create --name plain alpine", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\n")
	b.ok("docker build -t scratch-img:1 -t other-img .", "ok")
	b.ok("docker volume create scratch-vol", "scratch-vol\n")
	b.ok("docker volume create --label bead=pg2-04xmm named-vol", "named-vol\n")
	b.ok("docker run -d alpine sleep 600", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n")
	// A creation that failed (name clash with someone else's object) proves nothing.
	b.call("docker run -d --name victim alpine", "Conflict. The container name is already in use", true)
	// A docker command only MENTIONED inside another command is not a docker leaf.
	b.ok(`echo "docker run --name decoy alpine"`, "docker run --name decoy alpine\n")
	return b.write()
}

func TestOwnedApproved(t *testing.T) {
	tr := sessionTranscript(t)
	for _, cmd := range []string{
		"docker rm mine",
		"docker rm -f mine",
		"docker rm -f -v mine plain",
		"docker container rm mine",
		"docker container remove plain",
		"docker stop mine",
		"docker stop -t 5 mine",
		"docker stop --time=5 mine",
		"docker container stop mine plain",
		"docker rm 0123456789ab",   // id prefix of a container the session started
		"docker stop " + fullID,    // full id
		"docker rm aaaaaaaaaaaa",   // unlabeled `run -d`, proven by its printed id
		"docker rmi scratch-img:1", // tagged
		"docker rmi -f other-img",  // untagged ref means :latest
		"docker rmi other-img:latest",
		"docker image rm scratch-img:1",
		"docker image remove other-img",
		"docker volume rm scratch-vol",
		"docker volume rm -f scratch-vol named-vol",
		"docker volume remove named-vol",
		"/usr/local/bin/docker rm mine",
	} {
		t.Run(cmd, func(t *testing.T) {
			res, err := evalDocker(t, cmd, tr)
			if err != nil || res.Decision != hookio.Approve {
				t.Fatalf("got (%v, %v), want Approve", res.Decision, err)
			}
		})
	}
}

func TestOwnedNotApproved(t *testing.T) {
	tr := sessionTranscript(t)
	cases := map[string]string{
		// objects the session did not create
		"unknown container":         "docker rm someone-elses",
		"unknown container stop":    "docker stop someone-elses",
		"unknown container kind":    "docker container rm someone-elses",
		"unknown image":             "docker rmi postgres:16",
		"unknown image rm":          "docker image rm postgres:16",
		"unknown volume":            "docker volume rm pgdata",
		"image tag differs":         "docker rmi scratch-img:2",
		"id prefix of nothing":      "docker rm deadbeefdead",
		"too short id prefix":       "docker rm 01234",
		"mixed owned and unowned":   "docker rm mine someone-elses",
		"mixed volumes":             "docker volume rm scratch-vol pgdata",
		"failed creation":           "docker rm victim",
		"only mentioned in echo":    "docker rm decoy",
		"container name as image":   "docker rmi mine",
		"image name as container":   "docker rm scratch-img",
		"volume name as container":  "docker rm scratch-vol",
		"container name as volume":  "docker volume rm mine",
		"owned container, wrong id": "docker rm 0123456789ac",
		// shapes whose targets cannot be proven
		"command substitution": "docker rm $(docker ps -aq)",
		"backticks":            "docker rm `docker ps -aq`",
		"glob":                 "docker rm mine*",
		"variable":             "docker rm $C",
		"no target":            "docker rm -f",
		"unknown flag":         "docker rm --frobnicate mine",
		"filter flag":          "docker rm --filter name=mine",
		"global host flag":     "docker -H tcp://other:2375 rm mine",
		"global context flag":  "docker --context prod stop mine",
		"global debug flag":    "docker --debug rm mine",
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := evalDocker(t, cmd, tr)
			if err == nil && res.Decision == hookio.Approve {
				t.Fatalf("%q approved, want not approved", cmd)
			}
			// A refusal (ADR 0044 floor), never a quiet not-applicable, so a later
			// rule in the chain cannot widen it into an Approve.
			if !errors.Is(err, hookio.ErrRefused) {
				t.Fatalf("%q: err = %v, want a refusal", cmd, err)
			}
		})
	}
}

// Without a transcript nothing is provable: even a name that WOULD be owned is
// not approved, and an unreadable path behaves the same.
func TestOwnedNoTranscript(t *testing.T) {
	for _, tr := range []string{"", filepath.Join(t.TempDir(), "missing.jsonl")} {
		for _, cmd := range []string{"docker rm mine", "docker stop mine", "docker rmi scratch-img:1", "docker volume rm scratch-vol"} {
			res, err := evalDocker(t, cmd, tr)
			if err == nil && res.Decision == hookio.Approve {
				t.Fatalf("%q approved with transcript %q", cmd, tr)
			}
		}
	}
}

// The prune verbs are never approved, with or without a transcript that owns
// everything on the engine, and with or without -f / --all.
func TestPruneNeverApproved(t *testing.T) {
	tr := sessionTranscript(t)
	for _, cmd := range []string{
		"docker system prune",
		"docker system prune -af --volumes",
		"docker container prune",
		"docker container prune -f",
		"docker image prune",
		"docker image prune -a -f",
		"docker volume prune",
		"docker volume prune -f --all",
		"docker network prune -f",
		"docker builder prune -af",
	} {
		for _, transcript := range []string{tr, ""} {
			t.Run(cmd, func(t *testing.T) {
				res, err := evalDocker(t, cmd, transcript)
				if err == nil && res.Decision == hookio.Approve {
					t.Fatalf("%q approved", cmd)
				}
				if !errors.Is(err, hookio.ErrRefused) {
					t.Fatalf("%q: err = %v, want a refusal", cmd, err)
				}
			})
		}
	}
}

// Non-destructive verbs keep their unconditional approval.
func TestNonDestructiveVerbsStillApproved(t *testing.T) {
	for _, cmd := range []string{"docker ps -a", "docker images", "docker logs c1", "docker inspect c1", "docker start c1", "docker build -t x ."} {
		res, err := evalDocker(t, cmd, "")
		if err != nil || res.Decision != hookio.Approve {
			t.Fatalf("%q: got (%v, %v), want Approve", cmd, res.Decision, err)
		}
	}
}

func TestNormalizeImage(t *testing.T) {
	for in, want := range map[string]string{
		"img":                      "img:latest",
		"img:1":                    "img:1",
		"reg.example:5000/img":     "reg.example:5000/img:latest",
		"reg.example:5000/img:2":   "reg.example:5000/img:2",
		"img@sha256:" + fullID[:8]: "img@sha256:" + fullID[:8],
	} {
		if got := normalizeImage(in); got != want {
			t.Errorf("normalizeImage(%q) = %q, want %q", in, got, want)
		}
	}
}
