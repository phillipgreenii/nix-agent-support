"""Configuration service for gh-prreview."""

from __future__ import annotations

import os
import subprocess
from pathlib import Path

from gh_prreview.models.config import ConfigurationError, GhPrreviewConfig


class ConfigService:
    """Load configuration from environment variables or gh config.

    No hardcoded defaults for paths - user must configure them.

    Configuration sources (in order of precedence):
    1. Environment variables (GH_PRREVIEW_*)
    2. gh config values (prreview.*)
    """

    def load_config(self) -> GhPrreviewConfig:
        """Load configuration.

        Raises:
            ConfigurationError: If required configuration is missing
        """
        review_path = self._get_required_path(
            env_var="GH_PRREVIEW_REVIEW_PATH",
            gh_config_key="prreview.review-path",
            description="review worktree path",
        )

        return GhPrreviewConfig(
            review_path=review_path,
            old_pr_days=self._get_int(
                "GH_PRREVIEW_OLD_PR_DAYS", "prreview.old-pr-days", default=30
            ),
            watch_labels=self._get_list("GH_PRREVIEW_WATCH_LABELS", "prreview.watch-labels"),
            watch_users=self._get_list("GH_PRREVIEW_WATCH_USERS", "prreview.watch-users"),
            debug=self._get_bool("GH_PRREVIEW_DEBUG", "prreview.debug", default=False),
        )

    def _get_from_env_or_gh_config(self, env_var: str, gh_config_key: str) -> str | None:
        """Get value from environment variable or gh config.

        Returns None if not found in either location.
        """
        # Check environment first
        env_value = os.environ.get(env_var)
        if env_value:
            return env_value

        # Fall back to gh config
        try:
            result = subprocess.run(
                ["gh", "config", "get", gh_config_key], capture_output=True, text=True
            )
            if result.returncode == 0 and result.stdout.strip():
                return result.stdout.strip()
        except Exception:
            pass

        return None

    def _get_required_path(self, env_var: str, gh_config_key: str, description: str) -> Path:
        """Get a required path configuration value.

        Raises:
            ConfigurationError: If the value is not set
        """
        value = self._get_from_env_or_gh_config(env_var, gh_config_key)

        if not value:
            raise ConfigurationError(
                f"Required configuration missing: {description}\n\n"
                f"Set one of:\n"
                f"  • Environment variable: {env_var}\n"
                f"  • gh config: gh config set {gh_config_key} <path>\n"
            )

        return Path(value).expanduser()

    def _get_int(self, env_var: str, gh_config_key: str, default: int) -> int:
        """Get an integer configuration value with default."""
        value = self._get_from_env_or_gh_config(env_var, gh_config_key)
        if value:
            try:
                return int(value)
            except ValueError:
                pass
        return default

    def _get_bool(self, env_var: str, gh_config_key: str, default: bool) -> bool:
        """Get a boolean configuration value with default."""
        value = self._get_from_env_or_gh_config(env_var, gh_config_key)
        if value:
            return value.lower() in ("true", "1", "yes")
        return default

    def _get_list(self, env_var: str, gh_config_key: str) -> list[str]:
        """Get a comma-separated list configuration value."""
        value = self._get_from_env_or_gh_config(env_var, gh_config_key)
        if value:
            return [item.strip() for item in value.split(",") if item.strip()]
        return []
