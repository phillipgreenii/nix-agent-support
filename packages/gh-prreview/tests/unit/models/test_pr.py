"""Tests for PR model and status determination."""

from datetime import UTC, datetime

import pytest

from gh_prreview.models.pr import PullRequest, Review, ReviewState, ReviewStatus


class TestReviewStatusDetermination:
    """Tests for PullRequest.get_user_review_status()."""

    def test_new_pr_no_reviews(self, sample_pr: PullRequest) -> None:
        """PR with no reviews should have NEW status."""
        status = sample_pr.get_user_review_status("testuser")
        assert status == ReviewStatus.NEW

    def test_stale_review_approved(self, pr_with_stale_review: PullRequest) -> None:
        """Approved review on old commit should show STALE_REVIEW."""
        status = pr_with_stale_review.get_user_review_status("testuser")
        assert status == ReviewStatus.STALE_REVIEW

    def test_current_review_returns_none(self, pr_with_current_review: PullRequest) -> None:
        """Review on latest commit should return None (skip PR)."""
        status = pr_with_current_review.get_user_review_status("testuser")
        assert status is None

    def test_pending_review(self, pr_with_pending_review: PullRequest) -> None:
        """Pending review should show PENDING_REVIEW."""
        status = pr_with_pending_review.get_user_review_status("testuser")
        assert status == ReviewStatus.PENDING_REVIEW

    def test_pending_review_takes_precedence_over_stale(self) -> None:
        """If user has both pending and stale reviews, pending wins."""
        pr = PullRequest(
            number=123,
            title="Test",
            author="alice",
            is_draft=False,
            created_at=datetime.now(UTC),
            updated_at=datetime.now(UTC),
            head_ref_oid="new123",
            reviews=[
                Review(author="testuser", state=ReviewState.APPROVED, commit_id="old123"),
                Review(author="testuser", state=ReviewState.PENDING, commit_id="new123"),
            ],
            labels=[],
        )
        status = pr.get_user_review_status("testuser")
        assert status == ReviewStatus.PENDING_REVIEW

    @pytest.mark.parametrize(
        "review_state",
        [
            ReviewState.APPROVED,
            ReviewState.CHANGES_REQUESTED,
            ReviewState.COMMENTED,
        ],
    )
    def test_all_submitted_states_can_be_stale(self, review_state: ReviewState) -> None:
        """All submitted review states should show stale if commit changed."""
        pr = PullRequest(
            number=123,
            title="Test",
            author="alice",
            is_draft=False,
            created_at=datetime.now(UTC),
            updated_at=datetime.now(UTC),
            head_ref_oid="new456",
            reviews=[
                Review(author="testuser", state=review_state, commit_id="old123"),
            ],
            labels=[],
        )
        status = pr.get_user_review_status("testuser")
        assert status == ReviewStatus.STALE_REVIEW

    def test_other_user_review_ignored(self) -> None:
        """Reviews from other users should not affect status."""
        pr = PullRequest(
            number=123,
            title="Test",
            author="alice",
            is_draft=False,
            created_at=datetime.now(UTC),
            updated_at=datetime.now(UTC),
            head_ref_oid="abc123",
            reviews=[
                Review(author="otheruser", state=ReviewState.APPROVED, commit_id="abc123"),
            ],
            labels=[],
        )
        status = pr.get_user_review_status("testuser")
        assert status == ReviewStatus.NEW  # User hasn't reviewed


class TestPullRequestURL:
    """Tests for PR URL generation."""

    def test_url_generation(self) -> None:
        """PR URL should be properly formatted."""
        pr = PullRequest(
            number=123,
            title="Test",
            author="alice",
            is_draft=False,
            created_at=datetime.now(UTC),
            updated_at=datetime.now(UTC),
            head_ref_oid="abc123",
            reviews=[],
            labels=[],
            owner="test-owner",
            repo="test-repo",
        )
        assert pr.url == "https://github.com/test-owner/test-repo/pull/123"
