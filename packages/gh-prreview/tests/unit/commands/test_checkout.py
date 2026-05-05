"""Tests for checkout command."""

from __future__ import annotations

from unittest.mock import AsyncMock, Mock

import pytest
from rich.console import Console

from gh_prreview.commands.checkout import CheckoutCommand
from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.pr import PullRequest


@pytest.fixture
def mock_github_service():
    """Mock GitHub service."""
    service = AsyncMock()
    return service


@pytest.fixture
def mock_git_service():
    """Mock Git service."""
    service = Mock()
    return service


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


@pytest.fixture
def sample_pr():
    """Sample pull request."""
    from datetime import UTC, datetime

    return PullRequest(
        number=12345,
        title="Test PR",
        author="testauthor",
        is_draft=False,
        created_at=datetime.now(UTC),
        updated_at=datetime.now(UTC),
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )


@pytest.mark.asyncio
async def test_checkout_success(mock_github_service, mock_git_service, config, console, sample_pr):
    """Test successful PR checkout."""
    mock_github_service.get_pr.return_value = sample_pr

    command = CheckoutCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", 12345)

    assert exit_code == 0
    mock_github_service.get_pr.assert_called_once_with("owner", "repo", 12345)
    mock_git_service.fetch_pr.assert_called_once_with(12345, "owner", "repo")
    mock_git_service.create_worktree.assert_called_once()

    # Verify worktree path
    call_args = mock_git_service.create_worktree.call_args
    assert call_args[0][0] == config.review_path / "pr-12345"
    assert call_args[0][1] == "review/pr-12345"
    assert call_args[0][2] == "origin/pr/12345"


@pytest.mark.asyncio
async def test_checkout_worktree_already_exists(
    mock_github_service, mock_git_service, config, console, sample_pr
):
    """Test checkout when worktree already exists."""
    mock_github_service.get_pr.return_value = sample_pr

    # Create the worktree directory
    worktree_path = config.review_path / "pr-12345"
    worktree_path.mkdir(parents=True)

    command = CheckoutCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", 12345)

    assert exit_code == 0
    mock_github_service.get_pr.assert_called_once()
    # Should not fetch or create worktree
    mock_git_service.fetch_pr.assert_not_called()
    mock_git_service.create_worktree.assert_not_called()


@pytest.mark.asyncio
async def test_checkout_github_error(mock_github_service, mock_git_service, config, console):
    """Test checkout when GitHub API fails."""
    mock_github_service.get_pr.side_effect = Exception("API error")

    command = CheckoutCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", 12345)

    assert exit_code == 1
    mock_git_service.fetch_pr.assert_not_called()
    mock_git_service.create_worktree.assert_not_called()


@pytest.mark.asyncio
async def test_checkout_git_fetch_error(
    mock_github_service, mock_git_service, config, console, sample_pr
):
    """Test checkout when git fetch fails."""
    mock_github_service.get_pr.return_value = sample_pr
    mock_git_service.fetch_pr.side_effect = Exception("Git fetch failed")

    command = CheckoutCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", 12345)

    assert exit_code == 1
    mock_git_service.create_worktree.assert_not_called()


@pytest.mark.asyncio
async def test_checkout_worktree_creation_error(
    mock_github_service, mock_git_service, config, console, sample_pr
):
    """Test checkout when worktree creation fails."""
    mock_github_service.get_pr.return_value = sample_pr
    mock_git_service.create_worktree.side_effect = Exception("Worktree creation failed")

    command = CheckoutCommand(mock_github_service, mock_git_service, config, console)
    exit_code = await command.execute("owner", "repo", 12345)

    assert exit_code == 1


@pytest.mark.asyncio
async def test_checkout_debug_mode(mock_github_service, mock_git_service, tmp_path, console):
    """Test checkout error handling in debug mode."""
    debug_config = GhPrreviewConfig(
        review_path=tmp_path / "reviews",
        watch_labels=["team/test"],
        watch_users=["testuser"],
        old_pr_days=30,
        debug=True,
    )
    mock_github_service.get_pr.side_effect = Exception("Test error")

    command = CheckoutCommand(mock_github_service, mock_git_service, debug_config, console)
    exit_code = await command.execute("owner", "repo", 12345)

    assert exit_code == 1
