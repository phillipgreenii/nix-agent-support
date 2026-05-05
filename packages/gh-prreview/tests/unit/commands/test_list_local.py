"""Tests for list-local command."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import Mock

import pytest
from rich.console import Console

from gh_prreview.commands.list_local import ListLocalCommand
from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.worktree import Worktree


@pytest.fixture
def mock_git_service():
    """Mock Git service."""
    return Mock()


@pytest.fixture
def config(tmp_path):
    """Test configuration."""
    return GhPrreviewConfig(
        review_path=tmp_path / "reviews",
        watch_labels=["team/test"],
        watch_users=["testuser"],
        old_pr_days=30,
        debug=False,
    )


@pytest.fixture
def console():
    """Mock console."""
    return Console(force_terminal=False, force_interactive=False)


def test_list_local_no_review_path(mock_git_service, config, console):
    """Test list-local when review path doesn't exist."""
    command = ListLocalCommand(mock_git_service, config, console)
    exit_code = command.execute()

    assert exit_code == 0
    mock_git_service.get_worktree_info.assert_not_called()


def test_list_local_empty_review_path(mock_git_service, config, console):
    """Test list-local when review path is empty."""
    config.review_path.mkdir(parents=True)

    command = ListLocalCommand(mock_git_service, config, console)
    exit_code = command.execute()

    assert exit_code == 0
    mock_git_service.get_worktree_info.assert_not_called()


def test_list_local_with_worktrees(mock_git_service, config, console):
    """Test list-local with multiple worktrees."""
    config.review_path.mkdir(parents=True)

    # Create worktree directories
    (config.review_path / "pr-12345").mkdir()
    (config.review_path / "pr-67890").mkdir()
    (config.review_path / "not-a-pr").mkdir()  # Should be ignored

    # Mock worktree info
    worktree1 = Worktree(
        pr_number=12345,
        path=config.review_path / "pr-12345",
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )
    worktree2 = Worktree(
        pr_number=67890,
        path=config.review_path / "pr-67890",
        branch_name="review/pr-67890",
        has_uncommitted_changes=True,
        unpushed_commit_count=2,
    )

    def get_worktree_info(path: Path):
        if path.name == "pr-12345":
            return worktree1
        elif path.name == "pr-67890":
            return worktree2
        return None

    mock_git_service.get_worktree_info.side_effect = get_worktree_info

    command = ListLocalCommand(mock_git_service, config, console)
    exit_code = command.execute()

    assert exit_code == 0
    assert mock_git_service.get_worktree_info.call_count == 2


def test_list_local_with_clean_worktree(mock_git_service, config, console):
    """Test list-local with a clean worktree."""
    config.review_path.mkdir(parents=True)
    (config.review_path / "pr-12345").mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=config.review_path / "pr-12345",
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree

    command = ListLocalCommand(mock_git_service, config, console)
    exit_code = command.execute()

    assert exit_code == 0


def test_list_local_with_uncommitted_changes(mock_git_service, config, console):
    """Test list-local with uncommitted changes."""
    config.review_path.mkdir(parents=True)
    (config.review_path / "pr-12345").mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=config.review_path / "pr-12345",
        branch_name="review/pr-12345",
        has_uncommitted_changes=True,
        unpushed_commit_count=0,
    )

    mock_git_service.get_worktree_info.return_value = worktree

    command = ListLocalCommand(mock_git_service, config, console)
    exit_code = command.execute()

    assert exit_code == 0


def test_list_local_with_unpushed_commits(mock_git_service, config, console):
    """Test list-local with unpushed commits."""
    config.review_path.mkdir(parents=True)
    (config.review_path / "pr-12345").mkdir()

    worktree = Worktree(
        pr_number=12345,
        path=config.review_path / "pr-12345",
        branch_name="review/pr-12345",
        has_uncommitted_changes=False,
        unpushed_commit_count=3,
    )

    mock_git_service.get_worktree_info.return_value = worktree

    command = ListLocalCommand(mock_git_service, config, console)
    exit_code = command.execute()

    assert exit_code == 0


def test_list_local_sorted_by_pr_number(mock_git_service, config, console):
    """Test that worktrees are sorted by PR number."""
    config.review_path.mkdir(parents=True)
    (config.review_path / "pr-99999").mkdir()
    (config.review_path / "pr-11111").mkdir()
    (config.review_path / "pr-55555").mkdir()

    worktrees = [
        Worktree(
            pr_number=99999,
            path=config.review_path / "pr-99999",
            branch_name="review/pr-99999",
            has_uncommitted_changes=False,
            unpushed_commit_count=0,
        ),
        Worktree(
            pr_number=11111,
            path=config.review_path / "pr-11111",
            branch_name="review/pr-11111",
            has_uncommitted_changes=False,
            unpushed_commit_count=0,
        ),
        Worktree(
            pr_number=55555,
            path=config.review_path / "pr-55555",
            branch_name="review/pr-55555",
            has_uncommitted_changes=False,
            unpushed_commit_count=0,
        ),
    ]

    def get_worktree_info(path: Path):
        for wt in worktrees:
            if wt.path == path:
                return wt
        return None

    mock_git_service.get_worktree_info.side_effect = get_worktree_info

    command = ListLocalCommand(mock_git_service, config, console)
    exit_code = command.execute()

    assert exit_code == 0
