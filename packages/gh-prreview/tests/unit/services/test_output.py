"""Tests for output service."""

from __future__ import annotations

from datetime import UTC, datetime, timedelta

from rich.console import Console

from gh_prreview.models.pr import PullRequest, ReviewStatus
from gh_prreview.services.output import (
    add_pr_row,
    create_pr_table,
    format_relative_time,
    print_pr_table,
)


def test_format_relative_time_now():
    """Test formatting time that's just now."""
    now = datetime.now(UTC)
    assert format_relative_time(now) == "now"


def test_format_relative_time_minutes():
    """Test formatting time in minutes."""
    now = datetime.now(UTC)
    past = now - timedelta(minutes=5)
    assert format_relative_time(past) == "5m"


def test_format_relative_time_hours():
    """Test formatting time in hours."""
    now = datetime.now(UTC)
    past = now - timedelta(hours=3)
    assert format_relative_time(past) == "3h"


def test_format_relative_time_days():
    """Test formatting time in days."""
    now = datetime.now(UTC)
    past = now - timedelta(days=5)
    assert format_relative_time(past) == "5d"


def test_format_relative_time_months():
    """Test formatting time in months."""
    now = datetime.now(UTC)
    past = now - timedelta(days=60)
    assert format_relative_time(past) == "2mo"


def test_format_relative_time_years():
    """Test formatting time in years."""
    now = datetime.now(UTC)
    past = now - timedelta(days=400)
    assert format_relative_time(past) == "1y"


def test_create_pr_table_default():
    """Test creating PR table with default options."""
    table = create_pr_table()

    assert table.title == "Pull Requests"
    # Default includes status column
    assert len(table.columns) == 4  # PR, Title, Author, Status


def test_create_pr_table_with_status():
    """Test creating PR table with status column."""
    table = create_pr_table(show_status=True)

    assert len(table.columns) == 4  # PR, Title, Author, Status


def test_create_pr_table_with_age():
    """Test creating PR table with age column."""
    table = create_pr_table(show_age=True)

    # Default includes status, plus age
    assert len(table.columns) == 5  # PR, Title, Author, Status, Updated


def test_create_pr_table_with_all_columns():
    """Test creating PR table with all columns."""
    table = create_pr_table(show_status=True, show_age=True)

    assert len(table.columns) == 5  # PR, Title, Author, Status, Updated


def test_create_pr_table_custom_title():
    """Test creating PR table with custom title."""
    table = create_pr_table(title="Custom Title")

    assert table.title == "Custom Title"


def test_add_pr_row_basic():
    """Test adding a basic PR row."""
    now = datetime.now(UTC)
    pr = PullRequest(
        number=123,
        title="Test PR",
        author="testauthor",
        is_draft=False,
        created_at=now,
        updated_at=now,
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )

    table = create_pr_table(show_status=False, show_age=False)
    add_pr_row(table, pr)

    assert len(table.rows) == 1


def test_add_pr_row_with_status():
    """Test adding PR row with status."""
    now = datetime.now(UTC)
    pr = PullRequest(
        number=123,
        title="Test PR",
        author="testauthor",
        is_draft=False,
        created_at=now,
        updated_at=now,
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )

    table = create_pr_table(show_status=True, show_age=False)
    add_pr_row(table, pr, status=ReviewStatus.NEW)

    assert len(table.rows) == 1


def test_add_pr_row_with_age():
    """Test adding PR row with age."""
    now = datetime.now(UTC)
    pr = PullRequest(
        number=123,
        title="Test PR",
        author="testauthor",
        is_draft=False,
        created_at=now,
        updated_at=now - timedelta(hours=2),
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )

    table = create_pr_table(show_status=False, show_age=True)
    add_pr_row(table, pr, show_age=True)

    assert len(table.rows) == 1


def test_print_pr_table_empty():
    """Test printing empty PR table."""
    console = Console(force_terminal=False, force_interactive=False)
    print_pr_table(console, [], empty_message="No PRs")

    # Should print empty message without error


def test_print_pr_table_with_prs():
    """Test printing PR table with PRs."""
    console = Console(force_terminal=False, force_interactive=False)
    now = datetime.now(UTC)

    prs = [
        PullRequest(
            number=123,
            title="Test PR 1",
            author="author1",
            is_draft=False,
            created_at=now,
            updated_at=now,
            head_ref_oid="abc123",
            reviews=[],
            labels=[],
            owner="test",
            repo="repo",
        ),
        PullRequest(
            number=456,
            title="Test PR 2",
            author="author2",
            is_draft=False,
            created_at=now,
            updated_at=now,
            head_ref_oid="def456",
            reviews=[],
            labels=[],
            owner="test",
            repo="repo",
        ),
    ]

    print_pr_table(console, prs)

    # Should print table without error


def test_print_pr_table_with_status_tuples():
    """Test printing PR table with status tuples."""
    console = Console(force_terminal=False, force_interactive=False)
    now = datetime.now(UTC)

    pr = PullRequest(
        number=123,
        title="Test PR",
        author="author",
        is_draft=False,
        created_at=now,
        updated_at=now,
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )

    prs_with_status = [(pr, ReviewStatus.NEW)]

    print_pr_table(console, prs_with_status, show_status=True)

    # Should print table without error


def test_print_pr_table_custom_title():
    """Test printing PR table with custom title."""
    console = Console(force_terminal=False, force_interactive=False)
    now = datetime.now(UTC)

    pr = PullRequest(
        number=123,
        title="Test PR",
        author="author",
        is_draft=False,
        created_at=now,
        updated_at=now,
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )

    print_pr_table(console, [pr], title="Custom Title")

    # Should print table without error


def test_add_pr_row_all_status_types():
    """Test adding PR rows with all status types."""
    now = datetime.now(UTC)
    pr = PullRequest(
        number=123,
        title="Test PR",
        author="author",
        is_draft=False,
        created_at=now,
        updated_at=now,
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )

    for status in [ReviewStatus.NEW, ReviewStatus.STALE_REVIEW, ReviewStatus.PENDING_REVIEW]:
        table = create_pr_table(show_status=True)
        add_pr_row(table, pr, status=status)
        assert len(table.rows) == 1


def test_add_pr_row_color_coding_by_status():
    """Test that PR rows use correct color coding based on review status."""
    now = datetime.now(UTC)
    pr = PullRequest(
        number=123,
        title="Test PR",
        author="author",
        is_draft=False,
        created_at=now,
        updated_at=now,
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="test",
        repo="repo",
    )

    status_colors = {
        ReviewStatus.NEW: "green",
        ReviewStatus.STALE_REVIEW: "yellow",
        ReviewStatus.PENDING_REVIEW: "blue",
    }

    for status, expected_color in status_colors.items():
        table = create_pr_table(show_status=True)
        add_pr_row(table, pr, status=status)
        # Status is the 4th column (index 3); cells stored per-column
        status_cell = str(table.columns[3]._cells[0])
        assert f"[{expected_color}]" in status_cell
        assert status.value in status_cell
        assert f"[/{expected_color}]" in status_cell


def test_add_pr_row_pr_display_format():
    """Test PR display format includes correct link and metadata."""
    now = datetime.now(UTC)
    pr = PullRequest(
        number=456,
        title="My Feature PR",
        author="featuredev",
        is_draft=False,
        created_at=now,
        updated_at=now,
        head_ref_oid="abc123",
        reviews=[],
        labels=[],
        owner="myorg",
        repo="myrepo",
    )

    table = create_pr_table(show_status=False, show_age=False)
    add_pr_row(table, pr)

    # Cells are stored per-column; columns are PR, Title, Author
    pr_cell = str(table.columns[0]._cells[0])
    assert "456" in pr_cell
    assert "github.com/myorg/myrepo/pull/456" in pr_cell

    assert table.columns[1]._cells[0] == "My Feature PR"
    assert table.columns[2]._cells[0] == "featuredev"
