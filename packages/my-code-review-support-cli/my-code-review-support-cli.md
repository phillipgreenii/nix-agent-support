# my-code-review-support-cli

> Code review support utilities for AI agents. Manages PR worktrees and posts review comments to GitHub.

- Set up a worktree for reviewing a PR:

`my-code-review-support-cli setup {{pr_number_or_url}}`

- List changed files in the current worktree:

`my-code-review-support-cli files --base {{origin/main}}`

- List commits in the current worktree:

`my-code-review-support-cli commits --base {{origin/main}}`

- Get PR metadata:

`my-code-review-support-cli pr-info {{pr_number}}`

- Post review comments from JSON stdin:

`echo '{"comments":[...]}' | my-code-review-support-cli post {{pr_number}}`

- Clean up a worktree:

`my-code-review-support-cli cleanup {{/tmp/pr-review-12345}}`

- Show version:

`my-code-review-support-cli version`
