"""GitHub service for gh-prreview."""

from __future__ import annotations

import asyncio
import subprocess
from datetime import datetime
from typing import Any

import httpx

from gh_prreview.models.pr import PullRequest, Review, ReviewState


class GitHubService:
    """Async service for GitHub API operations.

    Uses direct API calls with httpx for better performance.
    Token is obtained once at startup from `gh auth token`.
    """

    GITHUB_API_URL = "https://api.github.com"
    GITHUB_GRAPHQL_URL = "https://api.github.com/graphql"

    def __init__(self, token: str | None = None):
        """Initialize with optional token (will fetch from gh if not provided)."""
        self._token = token or self._get_token_from_gh()
        self._client: httpx.AsyncClient | None = None

    @staticmethod
    def _get_token_from_gh() -> str:
        """Get GitHub token from gh CLI (called once at startup)."""
        result = subprocess.run(["gh", "auth", "token"], capture_output=True, text=True, check=True)
        return result.stdout.strip()

    async def __aenter__(self) -> GitHubService:
        """Async context manager entry."""
        self._client = httpx.AsyncClient(
            timeout=30.0,
            headers={
                "Authorization": f"Bearer {self._token}",
                "Accept": "application/vnd.github+json",
                "X-GitHub-Api-Version": "2022-11-28",
            },
            limits=httpx.Limits(max_connections=10),
        )
        return self

    async def __aexit__(self, *args: Any) -> None:
        """Async context manager exit."""
        if self._client:
            await self._client.aclose()

    async def get_current_user(self) -> str:
        """Get the current authenticated GitHub username."""
        assert self._client is not None
        response = await self._client.get(f"{self.GITHUB_API_URL}/user")
        response.raise_for_status()
        data: dict[str, str] = response.json()
        return data["login"]

    async def get_pr(self, owner: str, repo: str, number: int) -> PullRequest:
        """Get a single PR with full details including reviews."""
        assert self._client is not None

        # Use GraphQL to get PR + reviews in one request
        query = """
        query($owner: String!, $repo: String!, $number: Int!) {
          repository(owner: $owner, name: $repo) {
            pullRequest(number: $number) {
              number
              title
              author { login }
              isDraft
              createdAt
              updatedAt
              headRefOid
              state
              labels(first: 10) {
                nodes { name }
              }
              reviews(first: 50) {
                nodes {
                  author { login }
                  state
                  commit { oid }
                }
              }
            }
          }
        }
        """

        response = await self._client.post(
            self.GITHUB_GRAPHQL_URL,
            json={"query": query, "variables": {"owner": owner, "repo": repo, "number": number}},
        )
        response.raise_for_status()
        data = response.json()["data"]["repository"]["pullRequest"]
        return self._parse_graphql_pr(data, owner, repo)

    async def search_prs(
        self,
        query: str,
        limit: int = 50,
    ) -> list[PullRequest]:
        """Search for PRs using GitHub search API.

        Args:
            query: GitHub search query (e.g., "is:pr is:open review-requested:user")
            limit: Maximum results to return

        Returns:
            List of PRs (note: reviews/headRefOid may need separate fetch)
        """
        assert self._client is not None

        # Use REST API for search (GraphQL search is more limited)
        response = await self._client.get(
            f"{self.GITHUB_API_URL}/search/issues",
            params={
                "q": query,
                "per_page": min(limit, 100),
                "sort": "updated",
                "order": "desc",
            },
        )
        response.raise_for_status()

        items = response.json()["items"]
        # Note: Search results don't include reviews/headRefOid
        # Need to fetch full PR data for those
        return [self._parse_search_result(item) for item in items]

    async def search_prs_with_details(
        self,
        query: str,
        limit: int = 50,
    ) -> list[PullRequest]:
        """Search for PRs and fetch full details including reviews.

        Uses parallel requests for efficiency.
        """
        # First, get list of PR numbers from search
        basic_prs = await self.search_prs(query, limit)

        if not basic_prs:
            return []

        # Then fetch full details in parallel
        tasks = [self.get_pr(pr.owner, pr.repo, pr.number) for pr in basic_prs]

        results = await asyncio.gather(*tasks, return_exceptions=True)

        # Filter out errors and return successful results
        return [pr for pr in results if isinstance(pr, PullRequest)]

    def _parse_graphql_pr(self, data: dict[str, Any], owner: str, repo: str) -> PullRequest:
        """Parse GraphQL response into PullRequest model."""
        reviews = [
            Review(
                author=r["author"]["login"] if r["author"] else "ghost",
                state=ReviewState(r["state"]),
                commit_id=r["commit"]["oid"] if r.get("commit") else None,
            )
            for r in data.get("reviews", {}).get("nodes", [])
        ]

        return PullRequest(
            number=data["number"],
            title=data["title"],
            author=data["author"]["login"] if data["author"] else "ghost",
            is_draft=data.get("isDraft", False),
            created_at=datetime.fromisoformat(data["createdAt"].replace("Z", "+00:00")),
            updated_at=datetime.fromisoformat(data["updatedAt"].replace("Z", "+00:00")),
            head_ref_oid=data.get("headRefOid", ""),
            reviews=reviews,
            labels=[label["name"] for label in data.get("labels", {}).get("nodes", [])],
            owner=owner,
            repo=repo,
            state=data.get("state", "OPEN"),
        )

    def _parse_search_result(self, data: dict[str, Any]) -> PullRequest:
        """Parse REST search result (limited fields)."""
        # Extract owner/repo from html_url
        # e.g., https://github.com/owner/repo/pull/123
        url_parts = data["html_url"].split("/")
        owner = url_parts[-4]
        repo = url_parts[-3]

        return PullRequest(
            number=data["number"],
            title=data["title"],
            author=data["user"]["login"],
            is_draft=data.get("draft", False),
            created_at=datetime.fromisoformat(data["created_at"].replace("Z", "+00:00")),
            updated_at=datetime.fromisoformat(data["updated_at"].replace("Z", "+00:00")),
            head_ref_oid="",  # Not available in search results
            reviews=[],  # Not available in search results
            labels=[label["name"] for label in data.get("labels", [])],
            owner=owner,
            repo=repo,
            state=data.get("state", "open").upper(),
        )
