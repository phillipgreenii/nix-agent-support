# my-code-review-support-cli

Code review support CLI for AI agents. Manages PR worktrees and posts structured review comments to GitHub.

## Prerequisites

- `gh` CLI installed and authenticated (`gh auth login`)
- Git repository access

## Commands

| Command                 | Description                                             |
| ----------------------- | ------------------------------------------------------- |
| `setup <PR>`            | Identify PR, fetch branch, create worktree              |
| `files [--base BASE]`   | List changed files with stats                           |
| `commits [--base BASE]` | List commits with messages                              |
| `pr-info <PR_NUMBER>`   | Get full PR metadata                                    |
| `post <PR_NUMBER>`      | Parse JSON from stdin, deduplicate, post pending review |
| `cleanup <PATH>`        | Remove worktree                                         |
| `version`               | Print version                                           |

## Usage

```bash
# Review a PR
my-code-review-support-cli setup 12345
cd /tmp/pr-review-12345

# Get changed files and commits
my-code-review-support-cli files
my-code-review-support-cli commits

# Post review (reads JSON from stdin)
echo '{"comments":[{"path":"main.go","lines":[10],"message":"Bug","severity":"error"}]}' \
  | my-code-review-support-cli post 12345

# Clean up
my-code-review-support-cli cleanup /tmp/pr-review-12345
```

Run `my-code-review-support-cli post --help` for the full JSON schema documentation.

## Building

```bash
go build ./cmd/my-code-review-support-cli/
```

## Testing

```bash
go test ./...
```

## Dependencies

Go deps are not vendored. The Nix build uses `vendorHash` to fetch modules reproducibly. After changing Go dependencies (adding/removing imports, `go get -u`, etc.), refresh the hash:

```bash
./update-deps.sh
```

Or refresh everything at once via the workspace-level `../../update-locks.sh`. See [ADR 0035](../../docs/adr/0035-vendor-hash-with-nix-update-for-go-packages.md) for background.
