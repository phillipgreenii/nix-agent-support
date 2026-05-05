"""Pull Request models for gh-prreview."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from enum import Enum


class ReviewStatus(Enum):
    """Status of a PR from the user's review perspective."""

    NEW = "NEW"  # User hasn't interacted
    STALE_REVIEW = "STALE_REVIEW"  # New commits since user's review
    PENDING_REVIEW = "PENDING_REVIEW"  # User has draft review


class ReviewState(Enum):
    """GitHub review states."""

    APPROVED = "APPROVED"
    CHANGES_REQUESTED = "CHANGES_REQUESTED"
    COMMENTED = "COMMENTED"
    PENDING = "PENDING"


@dataclass
class Review:
    """A review on a PR."""

    author: str
    state: ReviewState
    commit_id: str | None = None


@dataclass
class PullRequest:
    """A GitHub Pull Request."""

    number: int
    title: str
    author: str
    is_draft: bool
    created_at: datetime
    updated_at: datetime
    head_ref_oid: str
    reviews: list[Review]
    labels: list[str]
    owner: str = ""  # Repository owner (e.g., "octocat")
    repo: str = ""  # Repository name (e.g., "hello-world")
    state: str = "OPEN"

    @property
    def url(self) -> str:
        """GitHub URL for this PR."""
        return f"https://github.com/{self.owner}/{self.repo}/pull/{self.number}"

    def get_user_review_status(self, username: str) -> ReviewStatus | None:
        """Determine the review status for a specific user.

        Returns:
            ReviewStatus if PR needs attention, None if user has reviewed latest commit
        """
        # Check for pending review first
        for review in self.reviews:
            if review.author == username and review.state == ReviewState.PENDING:
                return ReviewStatus.PENDING_REVIEW

        # Find user's last submitted review
        user_reviews = [
            r for r in self.reviews if r.author == username and r.state != ReviewState.PENDING
        ]

        if not user_reviews:
            return ReviewStatus.NEW

        last_review = user_reviews[-1]

        # Check if review is on latest commit
        if last_review.commit_id == self.head_ref_oid:
            return None  # User has reviewed latest - skip this PR

        return ReviewStatus.STALE_REVIEW
