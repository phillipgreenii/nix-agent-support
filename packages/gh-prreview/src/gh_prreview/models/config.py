"""Configuration models for gh-prreview."""

from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class GhPrreviewConfig:
    """Configuration for gh-prreview.

    All paths are required - no hardcoded defaults.
    Configuration is loaded from environment variables or gh config.
    """

    review_path: Path  # Required: where to create worktrees
    watch_labels: list[str]  # Labels to watch for review (can be empty)
    watch_users: list[str]  # Users to watch for review (can be empty)
    old_pr_days: int = 30  # Warning threshold for stale PRs
    debug: bool = False


class ConfigurationError(Exception):
    """Raised when required configuration is missing."""

    pass
