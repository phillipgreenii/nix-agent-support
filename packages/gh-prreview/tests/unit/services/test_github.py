"""Tests for GitHub service."""

from __future__ import annotations

from unittest.mock import AsyncMock, Mock, patch

import httpx
import pytest

from gh_prreview.models.pr import ReviewState
from gh_prreview.services.github import GitHubService


@pytest.fixture
def mock_httpx_client():
    """Mock httpx AsyncClient."""
    return AsyncMock(spec=httpx.AsyncClient)


@pytest.mark.asyncio
async def test_context_manager():
    """Test GitHubService as async context manager."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_client_class.return_value = mock_client

        async with GitHubService(token="test_token") as service:
            assert service._client is not None

        # Verify client was created
        mock_client_class.assert_called_once()


@pytest.mark.asyncio
async def test_get_current_user():
    """Test getting current user."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_response = Mock()
        mock_response.json.return_value = {"login": "testuser"}
        mock_response.raise_for_status = Mock()
        mock_client.get.return_value = mock_response
        mock_client_class.return_value = mock_client

        # Pass token directly to avoid gh auth call
        async with GitHubService(token="test_token") as service:
            user = await service.get_current_user()

            assert user == "testuser"
            mock_client.get.assert_called_once_with("https://api.github.com/user")


@pytest.mark.asyncio
async def test_search_prs():
    """Test searching for PRs."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_response = Mock()
        mock_response.json.return_value = {
            "items": [
                {
                    "number": 123,
                    "title": "Test PR",
                    "user": {"login": "author"},
                    "draft": False,
                    "created_at": "2024-01-01T00:00:00Z",
                    "updated_at": "2024-01-02T00:00:00Z",
                    "labels": [{"name": "bug"}],
                    "html_url": "https://github.com/test/repo/pull/123",
                }
            ]
        }
        mock_response.raise_for_status = Mock()
        mock_client.get.return_value = mock_response
        mock_client_class.return_value = mock_client

        async with GitHubService(token="test_token") as service:
            prs = await service.search_prs("is:pr is:open", limit=10)

            assert len(prs) == 1
            assert prs[0].number == 123
            assert prs[0].title == "Test PR"
            assert prs[0].author == "author"
            assert prs[0].is_draft is False


@pytest.mark.asyncio
async def test_search_prs_empty_results():
    """Test searching for PRs with no results."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_response = Mock()
        mock_response.json.return_value = {"items": []}
        mock_response.raise_for_status = Mock()
        mock_client.get.return_value = mock_response
        mock_client_class.return_value = mock_client

        async with GitHubService(token="test_token") as service:
            prs = await service.search_prs("is:pr is:open", limit=10)

            assert len(prs) == 0


@pytest.mark.asyncio
async def test_get_pr():
    """Test getting a specific PR."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_response = Mock()
        mock_response.json.return_value = {
            "data": {
                "repository": {
                    "pullRequest": {
                        "number": 123,
                        "title": "Test PR",
                        "author": {"login": "author"},
                        "isDraft": False,
                        "createdAt": "2024-01-01T00:00:00Z",
                        "updatedAt": "2024-01-02T00:00:00Z",
                        "headRefOid": "abc123",
                        "labels": {"nodes": [{"name": "bug"}]},
                        "state": "OPEN",
                        "reviews": {
                            "nodes": [
                                {
                                    "author": {"login": "reviewer"},
                                    "state": "APPROVED",
                                    "commit": {"oid": "abc123"},
                                }
                            ]
                        },
                    }
                }
            }
        }
        mock_response.raise_for_status = Mock()
        mock_client.post.return_value = mock_response
        mock_client_class.return_value = mock_client

        async with GitHubService(token="test_token") as service:
            pr = await service.get_pr("owner", "repo", 123)

            assert pr.number == 123
            assert pr.title == "Test PR"
            assert pr.author == "author"
            assert pr.head_ref_oid == "abc123"
            assert len(pr.reviews) == 1
            assert pr.reviews[0].author == "reviewer"
            assert pr.reviews[0].state == ReviewState.APPROVED


@pytest.mark.asyncio
async def test_get_pr_with_multiple_reviews():
    """Test getting PR with multiple reviews."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_response = Mock()
        mock_response.json.return_value = {
            "data": {
                "repository": {
                    "pullRequest": {
                        "number": 123,
                        "title": "Test PR",
                        "author": {"login": "author"},
                        "isDraft": False,
                        "createdAt": "2024-01-01T00:00:00Z",
                        "updatedAt": "2024-01-02T00:00:00Z",
                        "headRefOid": "abc123",
                        "labels": {"nodes": []},
                        "state": "OPEN",
                        "reviews": {
                            "nodes": [
                                {
                                    "author": {"login": "reviewer1"},
                                    "state": "APPROVED",
                                    "commit": {"oid": "abc123"},
                                },
                                {
                                    "author": {"login": "reviewer2"},
                                    "state": "CHANGES_REQUESTED",
                                    "commit": {"oid": "abc123"},
                                },
                            ]
                        },
                    }
                }
            }
        }
        mock_response.raise_for_status = Mock()
        mock_client.post.return_value = mock_response
        mock_client_class.return_value = mock_client

        async with GitHubService(token="test_token") as service:
            pr = await service.get_pr("owner", "repo", 123)

            assert len(pr.reviews) == 2
            assert pr.reviews[0].state == ReviewState.APPROVED
            assert pr.reviews[1].state == ReviewState.CHANGES_REQUESTED


@pytest.mark.asyncio
async def test_get_pr_http_error():
    """Test handling HTTP error when getting PR."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_response = Mock()
        mock_response.raise_for_status.side_effect = httpx.HTTPStatusError(
            "404 Not Found", request=Mock(), response=Mock()
        )
        mock_client.post.return_value = mock_response
        mock_client_class.return_value = mock_client

        async with GitHubService(token="test_token") as service:
            with pytest.raises(httpx.HTTPStatusError):
                await service.get_pr("owner", "repo", 123)


@pytest.mark.asyncio
async def test_search_prs_http_error():
    """Test handling HTTP error when searching PRs."""
    with patch("gh_prreview.services.github.httpx.AsyncClient") as mock_client_class:
        mock_client = AsyncMock()
        mock_response = Mock()
        mock_response.raise_for_status.side_effect = httpx.HTTPStatusError(
            "422 Unprocessable Entity", request=Mock(), response=Mock()
        )
        mock_client.get.return_value = mock_response
        mock_client_class.return_value = mock_client

        async with GitHubService(token="test_token") as service:
            with pytest.raises(httpx.HTTPStatusError):
                await service.search_prs("invalid query", limit=10)


@pytest.mark.asyncio
async def test_get_token_from_gh():
    """Test getting auth token from gh CLI."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout="ghp_token123\n", returncode=0)

        token = GitHubService._get_token_from_gh()

        assert token == "ghp_token123"
        mock_run.assert_called_once()
        assert "gh" in mock_run.call_args[0][0]
        assert "auth" in mock_run.call_args[0][0]
        assert "token" in mock_run.call_args[0][0]


@pytest.mark.asyncio
async def test_get_token_from_gh_error():
    """Test handling gh auth token error."""
    import subprocess

    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = subprocess.CalledProcessError(1, "gh")

        with pytest.raises(subprocess.CalledProcessError):
            GitHubService._get_token_from_gh()
