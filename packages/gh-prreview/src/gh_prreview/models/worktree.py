"""Worktree models for gh-prreview."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path


@dataclass
class Worktree:
    """A git worktree for PR review."""

    pr_number: int
    path: Path
    branch_name: str
    has_uncommitted_changes: bool = False
    unpushed_commit_count: int = 0
