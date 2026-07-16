package secretpath

import "testing"

func TestIsSecret(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		// Claude credential files (the motivating case, pg2-to8pe)
		{"claude credentials tilde", "~/.claude/.credentials", true},
		{"claude credentials absolute", "/Users/phillipg/.claude/.credentials", true},
		{"claude credentials json (linux oauth file)", "~/.claude/.credentials.json", true},
		{"claude auth.json", "~/.claude/auth.json", true},

		// secrets/ directory component (requires a path separator)
		{"secrets dir nested json", "secrets/gnosis/svc/foo.json", true},
		{"secrets dir absolute", "/repo/secrets/prod.yaml", true},
		{"secrets substring is not a component", "secretsauce/config.yaml", false},
		{"bare secrets word is not a path (kubectl get secrets)", "secrets", false},

		// .ssh/ directory component (requires a path separator)
		{"ssh config tilde", "~/.ssh/config", true},
		{"ssh private key", "~/.ssh/id_rsa", true},
		{"ssh dir itself", "~/.ssh", true},
		{"ssh known_hosts absolute", "/home/user/.ssh/known_hosts", true},
		{"ssh substring is not a component", "myssh/notes.txt", false},
		{"bare .ssh word without separator", ".ssh", false},

		// .env files
		{"bare dotenv", ".env", true},
		{"dotenv dot-slash", "./.env", true},
		{"dotenv nested", "project/.env", true},
		{"dotenv local", ".env.local", true},
		{"dotenv production", ".env.production", true},
		{"dotenv example is not secret", ".env.example", false},
		{"dotenv sample is not secret", ".env.sample", false},
		{"dotenv template is not secret", ".env.template", false},
		{"dotenv dist is not secret", ".env.dist", false},

		// *token*.json
		{"api token json", "config/api-token.json", true},
		{"github token json", "github-token.json", true},
		{"bare token json", "token.json", true},
		{"uppercase Token json", "Auth-Token.json", true},
		{"token but not json", "token.txt", false},

		// Non-secrets
		{"empty", "", false},
		{"go source", "internal/main.go", false},
		{"readme", "README.md", false},
		{"claude settings is not a secret", "~/.claude/settings.json", false},
		{"generic config json", "src/config.json", false},
		{"credentials with extra suffix is exact-match only", "~/.claude/.credentials.bak", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSecret(tt.path); got != tt.want {
				t.Errorf("IsSecret(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
