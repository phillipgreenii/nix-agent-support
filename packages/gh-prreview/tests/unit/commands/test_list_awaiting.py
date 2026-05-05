"""Tests for list-awaiting command."""

from datetime import UTC, datetime
from unittest.mock import AsyncMock, Mock

import pytest
from rich.console import Console

from gh_prreview.commands.list_awaiting import ListAwaitingCommand
from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.pr import PullRequest, ReviewStatus


class TestListAwaitingCommand:
    """Tests for ListAwaitingCommand."""

    @pytest.mark.asyncio
    async def test_gather_prs_returns_new_for_unreviewed(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
        sample_pr: PullRequest,
    ) -> None:
        """Unreviewed PRs should have NEW status."""
        mock_github_service.search_prs = AsyncMock(return_value=[sample_pr])

        console = Console()
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        result = await command.gather_prs("owner", "repo")

        assert len(result.prs) == 1
        pr, status = result.prs[0]
        assert pr.number == sample_pr.number
        assert status == ReviewStatus.NEW

    @pytest.mark.asyncio
    async def test_gather_prs_excludes_reviewed_latest(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
        pr_with_current_review: PullRequest,
    ) -> None:
        """PRs where user reviewed latest commit should be excluded."""
        mock_github_service.search_prs = AsyncMock(return_value=[pr_with_current_review])

        console = Console()
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        result = await command.gather_prs("owner", "repo")

        assert len(result.prs) == 0

    @pytest.mark.asyncio
    async def test_gather_prs_shows_stale_review(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
        pr_with_stale_review: PullRequest,
    ) -> None:
        """PRs with stale reviews should be included."""
        mock_github_service.search_prs = AsyncMock(return_value=[pr_with_stale_review])

        console = Console()
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        result = await command.gather_prs("owner", "repo")

        assert len(result.prs) == 1
        pr, status = result.prs[0]
        assert status == ReviewStatus.STALE_REVIEW

    @pytest.mark.asyncio
    async def test_gather_prs_deduplicates_across_sources(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
        sample_pr: PullRequest,
    ) -> None:
        """Same PR from multiple sources should appear once."""

        # Return same PR from multiple searches
        async def mock_search(query: str, limit: int) -> list[PullRequest]:
            return [sample_pr]

        mock_github_service.search_prs = mock_search

        console = Console()
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        result = await command.gather_prs("owner", "repo")

        assert len(result.prs) == 1

    @pytest.mark.asyncio
    async def test_gather_prs_fetches_missing_head_ref_oid(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
    ) -> None:
        """PRs missing headRefOid should trigger full PR fetch."""
        pr_without_oid = PullRequest(
            number=123,
            title="Test",
            author="alice",
            is_draft=False,
            created_at=datetime.now(UTC),
            updated_at=datetime.now(UTC),
            head_ref_oid="",  # Missing!
            reviews=[],
            labels=[],
        )

        pr_with_oid = PullRequest(
            number=123,
            title="Test",
            author="alice",
            is_draft=False,
            created_at=datetime.now(UTC),
            updated_at=datetime.now(UTC),
            head_ref_oid="abc123",  # Now has it
            reviews=[],
            labels=[],
        )

        mock_github_service.search_prs = AsyncMock(return_value=[pr_without_oid])
        mock_github_service.get_pr = AsyncMock(return_value=pr_with_oid)

        console = Console()
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        result = await command.gather_prs("owner", "repo")

        # Should have called get_pr to fetch full details
        mock_github_service.get_pr.assert_called_once()
        assert len(result.prs) == 1
        assert result.prs[0][0].head_ref_oid == "abc123"

    @pytest.mark.asyncio
    async def test_gather_prs_handles_search_errors_gracefully(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
    ) -> None:
        """Search errors should not crash the command."""

        async def mock_search(query: str, limit: int) -> list[PullRequest]:
            raise Exception("API error")

        mock_github_service.search_prs = mock_search

        console = Console()
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        result = await command.gather_prs("owner", "repo")

        # Should return empty list, not crash
        assert len(result.prs) == 0

    @pytest.mark.asyncio
    async def test_execute_returns_zero_on_success(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
        sample_pr: PullRequest,
    ) -> None:
        """execute() should return 0 on success."""
        mock_github_service.search_prs = AsyncMock(return_value=[sample_pr])

        console = Console(force_terminal=False, force_interactive=False)
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        exit_code = await command.execute("owner", "repo")

        assert exit_code == 0

    @pytest.mark.asyncio
    async def test_execute_displays_empty_message_when_no_prs(
        self,
        mock_github_service: Mock,
        sample_config: GhPrreviewConfig,
    ) -> None:
        """execute() should display empty message when no PRs found."""
        mock_github_service.search_prs = AsyncMock(return_value=[])

        console = Console(force_terminal=False, force_interactive=False)
        command = ListAwaitingCommand(mock_github_service, sample_config, console)

        exit_code = await command.execute("owner", "repo")

        assert exit_code == 0
