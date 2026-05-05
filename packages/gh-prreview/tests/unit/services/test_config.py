"""Tests for configuration loading."""

import subprocess
from pathlib import Path
from unittest.mock import Mock

import pytest

from gh_prreview.models.config import ConfigurationError
from gh_prreview.services.config import ConfigService


class TestConfigService:
    """Tests for ConfigService."""

    def test_missing_review_path_raises_error(self, monkeypatch: pytest.MonkeyPatch) -> None:
        """Missing review_path should raise ConfigurationError."""
        # Clear any existing config
        monkeypatch.delenv("GH_PRREVIEW_REVIEW_PATH", raising=False)

        # Mock gh config to return empty
        def mock_run(cmd, *args, **kwargs):  # type: ignore
            if cmd[:3] == ["gh", "config", "get"]:
                mock_result = Mock()
                mock_result.returncode = 1
                mock_result.stdout = ""
                return mock_result
            return subprocess.run(cmd, *args, **kwargs)

        monkeypatch.setattr(subprocess, "run", mock_run)

        service = ConfigService()

        with pytest.raises(ConfigurationError) as exc_info:
            service.load_config()

        assert "review worktree path" in str(exc_info.value)
        assert "GH_PRREVIEW_REVIEW_PATH" in str(exc_info.value)

    def test_review_path_from_environment(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """review_path should be loaded from environment variable."""
        review_path = tmp_path / "my-reviews"
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(review_path))

        service = ConfigService()
        config = service.load_config()

        assert config.review_path == review_path

    def test_watch_labels_parsed_as_list(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """Comma-separated labels should be parsed into list."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))
        monkeypatch.setenv("GH_PRREVIEW_WATCH_LABELS", "team/findev, team/jvm-guild, urgent")

        service = ConfigService()
        config = service.load_config()

        assert config.watch_labels == ["team/findev", "team/jvm-guild", "urgent"]

    def test_empty_watch_lists_are_valid(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """Empty watch_labels and watch_users are valid (not required)."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))
        # Explicitly clear watch_labels and watch_users
        monkeypatch.delenv("GH_PRREVIEW_WATCH_LABELS", raising=False)
        monkeypatch.delenv("GH_PRREVIEW_WATCH_USERS", raising=False)

        service = ConfigService()
        config = service.load_config()

        assert config.watch_labels == []
        assert config.watch_users == []

    def test_boolean_config_parsing(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """Boolean values should be parsed correctly."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))
        monkeypatch.setenv("GH_PRREVIEW_DEBUG", "true")

        service = ConfigService()
        config = service.load_config()

        assert config.debug is True

    def test_integer_config_parsing(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """Integer values should be parsed correctly."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))
        monkeypatch.setenv("GH_PRREVIEW_OLD_PR_DAYS", "45")

        service = ConfigService()
        config = service.load_config()

        assert config.old_pr_days == 45

    def test_path_expansion(
        self,
        monkeypatch: pytest.MonkeyPatch,
    ) -> None:
        """Paths with ~ should be expanded."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", "~/my-reviews")

        service = ConfigService()
        config = service.load_config()

        assert "~" not in str(config.review_path)
        assert config.review_path.is_absolute()

    def test_review_path_from_gh_config_fallback(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """review_path should fall back to gh config when env var is not set."""
        monkeypatch.delenv("GH_PRREVIEW_REVIEW_PATH", raising=False)

        def mock_run(cmd: list[str], *args: object, **kwargs: object) -> Mock:
            if cmd[:3] == ["gh", "config", "get"] and cmd[3] == "prreview.review-path":
                result = Mock()
                result.returncode = 0
                result.stdout = str(tmp_path / "gh-review-path")
                return result
            return subprocess.run(cmd, *args, **kwargs)

        monkeypatch.setattr(subprocess, "run", mock_run)

        service = ConfigService()
        config = service.load_config()

        assert config.review_path == tmp_path / "gh-review-path"

    def test_default_values_when_not_configured(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """old_pr_days and debug should use defaults when not set."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))
        monkeypatch.delenv("GH_PRREVIEW_OLD_PR_DAYS", raising=False)
        monkeypatch.delenv("GH_PRREVIEW_DEBUG", raising=False)

        def mock_run(cmd: list[str], *args: object, **kwargs: object) -> Mock:
            if cmd[:3] == ["gh", "config", "get"]:
                result = Mock()
                result.returncode = 1
                result.stdout = ""
                return result
            return subprocess.run(cmd, *args, **kwargs)

        monkeypatch.setattr(subprocess, "run", mock_run)

        service = ConfigService()
        config = service.load_config()

        assert config.old_pr_days == 30
        assert config.debug is False

    def test_watch_labels_from_gh_config_fallback(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """watch_labels should fall back to gh config when env var is not set."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))
        monkeypatch.delenv("GH_PRREVIEW_WATCH_LABELS", raising=False)

        def mock_run(cmd: list[str], *args: object, **kwargs: object) -> Mock:
            if cmd[:3] == ["gh", "config", "get"]:
                result = Mock()
                result.returncode = 1
                result.stdout = ""
                if cmd[3] == "prreview.watch-labels":
                    result.returncode = 0
                    result.stdout = "team/findev, team/jvm-guild"
                return result
            return subprocess.run(cmd, *args, **kwargs)

        monkeypatch.setattr(subprocess, "run", mock_run)

        service = ConfigService()
        config = service.load_config()

        assert config.watch_labels == ["team/findev", "team/jvm-guild"]

    def test_watch_users_from_gh_config_fallback(
        self,
        monkeypatch: pytest.MonkeyPatch,
        tmp_path: Path,
    ) -> None:
        """watch_users should fall back to gh config when env var is not set."""
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))
        monkeypatch.delenv("GH_PRREVIEW_WATCH_USERS", raising=False)

        def mock_run(cmd: list[str], *args: object, **kwargs: object) -> Mock:
            if cmd[:3] == ["gh", "config", "get"]:
                result = Mock()
                result.returncode = 1
                result.stdout = ""
                if cmd[3] == "prreview.watch-users":
                    result.returncode = 0
                    result.stdout = "alice, bob"
                return result
            return subprocess.run(cmd, *args, **kwargs)

        monkeypatch.setattr(subprocess, "run", mock_run)

        service = ConfigService()
        config = service.load_config()

        assert config.watch_users == ["alice", "bob"]
