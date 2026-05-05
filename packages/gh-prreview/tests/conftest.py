"""Shared fixtures for gh-prreview tests."""

from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import AsyncMock, Mock

import pytest

from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.pr import PullRequest, Review, ReviewState


@pytest.fixture
def sample_config(tmp_path: Path) -> GhPrreviewConfig:
    """Sample configuration for tests.

    Uses pytest's tmp_path fixture for review_path.
    """
    return GhPrreviewConfig(
        review_path=tmp_path / "reviews",
        watch_labels=["team/findev", "team/jvm-guild"],
        watch_users=["alice", "bob"],
        old_pr_days=30,
        debug=False,
    )


@pytest.fixture
def mock_github_service() -> Mock:
    """Mock GitHub service for testing."""
    service = Mock()
    service.get_current_user = AsyncMock(return_value="testuser")
    service.search_prs = AsyncMock(return_value=[])
    service.get_pr = AsyncMock()
    return service


@pytest.fixture
def mock_git_service() -> Mock:
    """Mock Git service for testing."""
    service = Mock()
    service.get_repo_from_remote = Mock(return_value=("owner", "repo"))
    service.create_worktree = Mock()
    service.remove_worktree = Mock()
    service.fetch_pr = Mock()
    service.delete_branch = Mock()
    service.prune_worktrees = Mock()
    service.get_worktree_info = Mock(return_value=None)
    service.has_uncommitted_changes = Mock(return_value=False)
    service.count_unpushed_commits = Mock(return_value=0)
    return service


@pytest.fixture
def sample_pr() -> PullRequest:
    """Sample PR for testing."""
    return PullRequest(
        number=123,
        title="Add new feature",
        author="alice",
        is_draft=False,
        created_at=datetime(2024, 1, 15, tzinfo=UTC),
        updated_at=datetime(2024, 1, 15, tzinfo=UTC),
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test-owner",
        repo="test-repo",
    )


@pytest.fixture
def pr_with_stale_review() -> PullRequest:
    """PR with a stale review (new commits since review)."""
    return PullRequest(
        number=456,
        title="Feature with stale review",
        author="alice",
        is_draft=False,
        created_at=datetime(2024, 1, 15, tzinfo=UTC),
        updated_at=datetime(2024, 1, 16, tzinfo=UTC),
        head_ref_oid="new456",  # Latest commit
        reviews=[Review(author="testuser", state=ReviewState.APPROVED, commit_id="old123")],
        labels=[],
        owner="test-owner",
        repo="test-repo",
    )


@pytest.fixture
def pr_with_current_review() -> PullRequest:
    """PR where user has reviewed the latest commit."""
    return PullRequest(
        number=789,
        title="Already reviewed",
        author="alice",
        is_draft=False,
        created_at=datetime(2024, 1, 15, tzinfo=UTC),
        updated_at=datetime(2024, 1, 15, tzinfo=UTC),
        head_ref_oid="abc123",
        reviews=[Review(author="testuser", state=ReviewState.APPROVED, commit_id="abc123")],
        labels=[],
        owner="test-owner",
        repo="test-repo",
    )


@pytest.fixture
def pr_with_pending_review() -> PullRequest:
    """PR with a pending (draft) review."""
    return PullRequest(
        number=101,
        title="Pending review",
        author="alice",
        is_draft=False,
        created_at=datetime(2024, 1, 15, tzinfo=UTC),
        updated_at=datetime(2024, 1, 15, tzinfo=UTC),
        head_ref_oid="abc123",
        reviews=[Review(author="testuser", state=ReviewState.PENDING, commit_id="abc123")],
        labels=[],
        owner="test-owner",
        repo="test-repo",
    )
