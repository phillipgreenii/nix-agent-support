"""List awaiting command for gh-prreview."""

from __future__ import annotations

import asyncio
from dataclasses import dataclass

from rich.console import Console

from gh_prreview.models.config import GhPrreviewConfig
from gh_prreview.models.pr import PullRequest, ReviewStatus
from gh_prreview.protocols import GitHubServiceProtocol
from gh_prreview.services.output import print_pr_table


@dataclass
class ListAwaitingResult:
    """Result of list-awaiting command."""

    prs: list[tuple[PullRequest, ReviewStatus]]
    current_user: str


class ListAwaitingCommand:
    """List PRs awaiting user's review."""

    def __init__(
        self,
        github_service: GitHubServiceProtocol,
        config: GhPrreviewConfig,
        console: Console,
    ):
        self.github = github_service
        self.config = config
        self.console = console

    async def execute(
        self,
        owner: str,
        repo: str,
        deep_search: bool = False,
        include_draft: bool = False,
    ) -> int:
        """Execute the list-awaiting command.

        Returns:
            Exit code (0 for success)
        """
        result = await self.gather_prs(owner, repo, deep_search, include_draft)
        self.display_results(result)
        return 0

    async def gather_prs(
        self,
        owner: str,
        repo: str,
        deep_search: bool = False,
        include_draft: bool = False,
    ) -> ListAwaitingResult:
        """Gather PRs awaiting review.

        This method is separated from execute() for easier testing.
        Uses parallel requests for efficiency.
        """
        current_user = await self.github.get_current_user()

        # Set limits based on deep search
        review_limit = 500 if deep_search else 50
        other_limit = 100 if deep_search else 20

        # Build search queries
        draft_filter = "" if include_draft else " draft:false"
        base_query = f"repo:{owner}/{repo} is:pr is:open{draft_filter} -author:{current_user}"

        queries = [
            # 1. PRs where review is requested from current user
            (f"{base_query} review-requested:{current_user}", review_limit),
        ]

        # 2. PRs from watched authors (combined into one query)
        if self.config.watch_users:
            author_query = " ".join(f"author:{u}" for u in self.config.watch_users)
            queries.append((f"{base_query} ({author_query})", other_limit))

        # 3. PRs with watched labels (combined into one query)
        if self.config.watch_labels:
            label_query = " ".join(f"label:{label}" for label in self.config.watch_labels)
            queries.append((f"{base_query} ({label_query})", other_limit))

        # Execute all searches in parallel
        search_tasks = [self.github.search_prs(query, limit) for query, limit in queries]
        search_results = await asyncio.gather(*search_tasks, return_exceptions=True)

        # Deduplicate by PR number
        all_prs: dict[int, PullRequest] = {}
        for result in search_results:
            if isinstance(result, list):
                for pr in result:
                    if pr.number not in all_prs:
                        all_prs[pr.number] = pr

        # Fetch full details for PRs missing headRefOid (in parallel)
        prs_needing_details = [pr for pr in all_prs.values() if not pr.head_ref_oid]

        if prs_needing_details:
            detail_tasks = [
                self.github.get_pr(owner, repo, pr.number) for pr in prs_needing_details
            ]
            detailed_results = await asyncio.gather(*detail_tasks, return_exceptions=True)

            # Update with full details (filter out exceptions)
            for item in detailed_results:
                if isinstance(item, PullRequest):
                    all_prs[item.number] = item

        # Determine status for each PR
        results: list[tuple[PullRequest, ReviewStatus]] = []
        for pr in all_prs.values():
            status = pr.get_user_review_status(current_user)
            if status is not None:  # None means user has reviewed latest commit
                results.append((pr, status))

        # Sort by status (NEW first, then STALE_REVIEW, then PENDING_REVIEW), then by author
        status_order = {
            ReviewStatus.NEW: 0,
            ReviewStatus.STALE_REVIEW: 1,
            ReviewStatus.PENDING_REVIEW: 2,
        }
        results.sort(key=lambda x: (status_order.get(x[1], 99), x[0].author.lower()))

        return ListAwaitingResult(prs=results, current_user=current_user)

    def display_results(self, result: ListAwaitingResult) -> None:
        """Display results as a Rich table with clickable PR links."""
        print_pr_table(
            self.console,
            result.prs,
            title="PRs Awaiting Review",
            show_status=True,
            show_age=True,
            empty_message="No PRs awaiting review.",
        )
