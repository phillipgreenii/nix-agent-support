// Fixture contents of the .git parser tests, as exported strings rather than data files: the
// engine's test runtime (`claude plugin test`) has no node:fs to read files with, and the nix
// check runs the same test files under node, so a module both can import is the common shape.
// Each value is the exact text of the file it stands for.

// .git/HEAD of a checkout on a branch.
export const HEAD_BRANCH = 'ref: refs/heads/feature/source-layer\n'

// .git/HEAD of a detached head.
export const HEAD_DETACHED = '0123456789abcdef0123456789abcdef01234567\n'

// A .git pointer file of a worktree, with an absolute git directory.
export const POINTER_ABSOLUTE = 'gitdir: /repos/main/.git/worktrees/wt1\n'

// A .git pointer file of a worktree, with a git directory relative to the worktree.
export const POINTER_RELATIVE = 'gitdir: ../main/.git/worktrees/wt2\n'
