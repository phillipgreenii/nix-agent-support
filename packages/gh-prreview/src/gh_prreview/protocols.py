"""Shared protocols for dependency injection in gh-prreview commands."""

from __future__ import annotations

from pathlib import Path
from typing import Protocol

from gh_prreview.models.pr import PullRequest
from gh_prreview.models.worktree import Worktree


class GitHubServiceProtocol(Protocol):
    """Protocol for GitHub service (enables easy mocking).

    This is the union of all GitHub operations used across commands.
    Individual commands may only use a subset of these methods.
    """

    async def get_current_user(self) -> str: ...
    async def search_prs(self, query: str, limit: int) -> list[PullRequest]: ...
    async def get_pr(self, owner: str, repo: str, number: int) -> PullRequest: ...


class GitServiceProtocol(Protocol):
    """Protocol for Git service (enables easy mocking).

    This is the union of all Git operations used across commands.
    Individual commands may only use a subset of these methods.
    """

    def create_worktree(self, target_path: Path, branch: str, start_point: str | None) -> None: ...
    def fetch_pr(self, pr_number: int, owner: str, repo: str) -> None: ...
    def get_worktree_info(self, path: Path) -> Worktree | None: ...
    def remove_worktree(self, path: Path, force: bool) -> None: ...
    def delete_branch(self, branch: str, force: bool) -> None: ...
    def prune_worktrees(self) -> None: ...
