package curl

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func makeInput(cmd string) *hookio.HookInput {
	return &hookio.HookInput{
		ToolName:  "Bash",
		CWD:       "/tmp",
		ToolInput: mustJSON(map[string]string{"command": cmd}),
	}
}

func TestCurl_ReadOnly_ZrOrg_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeInput("curl https://captains-log.zr.org/api/v1/builds/foo"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestCurl_ReadOnly_ZiprecruiterCom_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeInput("curl https://something.ziprecruiter.com/path"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestCurl_ReadOnly_WithFlags_Approve(t *testing.T) {
	r := New()
	cmds := []string{
		"curl -s https://captains-log.zr.org/api/v1/builds/foo",
		"curl -v https://captains-log.zr.org/api/v1/builds/foo",
		"curl -s -o /dev/null https://captains-log.zr.org/api/v1/builds/foo",
		"curl -X GET https://captains-log.zr.org/api/v1/builds/foo",
		"curl --request HEAD https://captains-log.zr.org/api/v1/builds/foo",
		"curl -XGET https://captains-log.zr.org/api/v1/builds/foo",
	}
	for _, cmd := range cmds {
		got := r.Evaluate(makeInput(cmd))
		if got.Decision != hookio.Approve {
			t.Errorf("cmd %q: got %s, want approve", cmd, got.Decision)
		}
	}
}

func TestCurl_NonZrDomain_Abstain(t *testing.T) {
	r := New()
	got := r.Evaluate(makeInput("curl https://evil.com/steal"))
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain (non-ZR domain)", got.Decision)
	}
}

func TestCurl_WriteMethod_Abstain(t *testing.T) {
	r := New()
	cmds := []string{
		"curl -X POST https://captains-log.zr.org/api",
		"curl --request DELETE https://captains-log.zr.org/api",
		"curl -XPUT https://captains-log.zr.org/api",
		"curl -XPATCH https://captains-log.zr.org/api",
	}
	for _, cmd := range cmds {
		got := r.Evaluate(makeInput(cmd))
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (write method)", cmd, got.Decision)
		}
	}
}

func TestCurl_DataFlags_Abstain(t *testing.T) {
	r := New()
	cmds := []string{
		`curl -d '{"key":"val"}' https://captains-log.zr.org/api`,
		`curl --data '{"key":"val"}' https://captains-log.zr.org/api`,
		`curl --data-raw 'foo' https://captains-log.zr.org/api`,
		`curl --data-binary @file https://captains-log.zr.org/api`,
		`curl --data-urlencode 'key=val' https://captains-log.zr.org/api`,
		`curl --json '{}' https://captains-log.zr.org/api`,
		`curl -F 'file=@upload.txt' https://captains-log.zr.org/api`,
		`curl --form 'field=value' https://captains-log.zr.org/api`,
		`curl -T localfile https://captains-log.zr.org/api`,
		`curl --upload-file localfile https://captains-log.zr.org/api`,
	}
	for _, cmd := range cmds {
		got := r.Evaluate(makeInput(cmd))
		if got.Decision != hookio.Abstain {
			t.Errorf("cmd %q: got %s, want abstain (write flag)", cmd, got.Decision)
		}
	}
}

func TestCurl_Pipeline_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeInput("curl https://captains-log.zr.org/api | jq '.'"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve (curl piped to jq)", got.Decision)
	}
}

func TestCurl_NoCurl_Abstain(t *testing.T) {
	r := New()
	got := r.Evaluate(makeInput("git status"))
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain (no curl in command)", got.Decision)
	}
}

func TestCurl_NonBashTool_Abstain(t *testing.T) {
	r := New()
	input := &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: mustJSON(map[string]string{"file_path": "/tmp/foo"}),
	}
	got := r.Evaluate(input)
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain (not Bash tool)", got.Decision)
	}
}

func TestCurl_MixedDomains_Abstain(t *testing.T) {
	// If a command has curl to both a ZR domain and an external domain, abstain
	r := New()
	got := r.Evaluate(makeInput("curl https://captains-log.zr.org/api && curl https://evil.com/data"))
	if got.Decision != hookio.Abstain {
		t.Errorf("got %s, want abstain (second curl targets non-ZR domain)", got.Decision)
	}
}

func TestCurl_Name(t *testing.T) {
	r := New()
	if got := r.Name(); got != "curl" {
		t.Errorf("Name() = %q, want curl", got)
	}
}

func TestCurl_ReadOnly_ZipawsCom_Approve(t *testing.T) {
	r := New()
	got := r.Evaluate(makeInput("curl https://something.zipaws.com/path"))
	if got.Decision != hookio.Approve {
		t.Errorf("got %s, want approve", got.Decision)
	}
}

func TestCurl_Localhost_Approve(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"localhost http", "curl http://localhost:8080/health", hookio.Approve},
		{"localhost https", "curl https://localhost/api/v1/status", hookio.Approve},
		{"127.0.0.1 http", "curl http://127.0.0.1:3000/metrics", hookio.Approve},
		{"127.0.0.1 https", "curl https://127.0.0.1/path", hookio.Approve},
		{"localhost write still abstains", "curl -X POST http://localhost:8080/api", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Evaluate(makeInput(tt.command))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

func TestCurl_DotLocalhost_Approve(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"phillipg.localhost", "curl https://phillipg.localhost/", hookio.Approve},
		{"service.localhost", "curl http://service.localhost:8080/health", hookio.Approve},
		{"deep.sub.localhost", "curl https://deep.sub.localhost/api", hookio.Approve},
		{"write to .localhost still abstains", "curl -X POST https://phillipg.localhost/api", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Evaluate(makeInput(tt.command))
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}

func TestCurl_ZrOrgSubdomains(t *testing.T) {
	r := New()
	tests := []struct {
		name    string
		command string
		want    hookio.Decision
	}{
		{"grafana.p1.zr.org", "curl https://grafana.p1.zr.org/api/dashboards", hookio.Approve},
		{"deep.sub.zr.org", "curl https://deep.sub.domain.zr.org/path", hookio.Approve},
		{"bare zr.org not matched", "curl https://zr.org/path", hookio.Abstain},
		{"evil-zr.org not matched", "curl https://evil-zr.org/path", hookio.Abstain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &hookio.HookInput{ToolName: "Bash", ToolInput: mustJSON(map[string]string{"command": tt.command})}
			got := r.Evaluate(input)
			if got.Decision != tt.want {
				t.Errorf("Decision = %v, want %v", got.Decision, tt.want)
			}
		})
	}
}
