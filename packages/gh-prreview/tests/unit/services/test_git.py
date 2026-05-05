"""Tests for git service."""

from __future__ import annotations

import subprocess
from unittest.mock import Mock, patch

import pytest

from gh_prreview.services.git import GitService


@pytest.fixture
def git_service():
    """Git service instance."""
    return GitService()


def test_get_repo_from_remote_https(git_service):
    """Test parsing HTTPS GitHub URL."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout="https://github.com/owner/repo.git\n", returncode=0)

        result = git_service.get_repo_from_remote()

        assert result == ("owner", "repo")


def test_get_repo_from_remote_ssh(git_service):
    """Test parsing SSH GitHub URL."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout="git@github.com:owner/repo.git\n", returncode=0)

        result = git_service.get_repo_from_remote()

        assert result == ("owner", "repo")


def test_get_repo_from_remote_no_git_extension(git_service):
    """Test parsing GitHub URL without .git extension."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout="https://github.com/owner/repo\n", returncode=0)

        result = git_service.get_repo_from_remote()

        assert result == ("owner", "repo")


def test_get_repo_from_remote_not_github(git_service):
    """Test parsing non-GitHub URL returns None."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout="https://gitlab.com/owner/repo.git\n", returncode=0)

        result = git_service.get_repo_from_remote()

        assert result is None


def test_get_repo_from_remote_git_error(git_service):
    """Test handling git command error."""
    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = git_service.get_repo_from_remote()

        assert result is None


def test_get_repo_from_remote_with_path(git_service, tmp_path):
    """Test get_repo_from_remote with explicit path."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout="https://github.com/owner/repo.git\n", returncode=0)

        result = git_service.get_repo_from_remote(tmp_path)

        assert result == ("owner", "repo")
        # Verify git was called with the correct path
        mock_run.assert_called_once()
        assert "-C" in mock_run.call_args[0][0]
        assert str(tmp_path) in mock_run.call_args[0][0]


def test_create_worktree(git_service, tmp_path):
    """Test creating a worktree."""
    with patch("subprocess.run") as mock_run:
        git_service.create_worktree(tmp_path / "worktree", "branch-name", "origin/main")

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert cmd[0] == "git"
        assert "worktree" in cmd
        assert "add" in cmd
        assert str(tmp_path / "worktree") in cmd
        assert "-b" in cmd
        assert "branch-name" in cmd
        assert "origin/main" in cmd


def test_create_worktree_no_start_point(git_service, tmp_path):
    """Test creating a worktree without start point."""
    with patch("subprocess.run") as mock_run:
        git_service.create_worktree(tmp_path / "worktree", "branch-name", None)

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert "origin/main" not in cmd


def test_remove_worktree(git_service, tmp_path):
    """Test removing a worktree."""
    with patch("subprocess.run") as mock_run:
        git_service.remove_worktree(tmp_path / "worktree", force=False)

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert cmd[0] == "git"
        assert "worktree" in cmd
        assert "remove" in cmd
        assert str(tmp_path / "worktree") in cmd
        assert "--force" not in cmd


def test_remove_worktree_force(git_service, tmp_path):
    """Test force removing a worktree."""
    with patch("subprocess.run") as mock_run:
        git_service.remove_worktree(tmp_path / "worktree", force=True)

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert "--force" in cmd


def test_prune_worktrees(git_service):
    """Test pruning worktrees."""
    with patch("subprocess.run") as mock_run:
        git_service.prune_worktrees()

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert cmd == ["git", "worktree", "prune"]


def test_list_worktrees(git_service):
    """Test listing worktrees."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(
            stdout="""worktree /path/to/main
HEAD abc123
branch refs/heads/main

worktree /path/to/feature
HEAD def456
branch refs/heads/feature
detached
""",
            returncode=0,
        )

        worktrees = git_service.list_worktrees()

        assert len(worktrees) == 2
        assert worktrees[0]["worktree"] == "/path/to/main"
        assert worktrees[0]["HEAD"] == "abc123"
        assert worktrees[0]["branch"] == "refs/heads/main"
        assert worktrees[1]["worktree"] == "/path/to/feature"
        assert worktrees[1]["HEAD"] == "def456"


def test_has_uncommitted_changes_true(git_service, tmp_path):
    """Test detecting uncommitted changes."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout=" M file.txt\n", returncode=0)

        result = git_service.has_uncommitted_changes(tmp_path)

        assert result is True


def test_has_uncommitted_changes_false(git_service, tmp_path):
    """Test clean worktree."""
    with patch("subprocess.run") as mock_run:
        mock_run.return_value = Mock(stdout="", returncode=0)

        result = git_service.has_uncommitted_changes(tmp_path)

        assert result is False


def test_has_uncommitted_changes_error(git_service, tmp_path):
    """Test handling git status error."""
    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = git_service.has_uncommitted_changes(tmp_path)

        assert result is False


def test_count_unpushed_commits(git_service, tmp_path):
    """Test counting unpushed commits."""
    with patch("subprocess.run") as mock_run:
        # First call checks upstream exists, second counts commits
        mock_run.side_effect = [
            Mock(returncode=0),  # upstream exists
            Mock(stdout="3\n", returncode=0),  # 3 commits ahead
        ]

        result = git_service.count_unpushed_commits(tmp_path)

        assert result == 3


def test_count_unpushed_commits_no_upstream(git_service, tmp_path):
    """Test counting unpushed commits with no upstream."""
    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = git_service.count_unpushed_commits(tmp_path)

        assert result == 0


def test_fetch_pr(git_service):
    """Test fetching a PR."""
    with patch("subprocess.run") as mock_run:
        git_service.fetch_pr(12345, "owner", "repo")

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert cmd[0] == "git"
        assert "fetch" in cmd
        assert "origin" in cmd
        assert "pull/12345/head:refs/remotes/origin/pr/12345" in cmd


def test_delete_branch(git_service):
    """Test deleting a branch."""
    with patch("subprocess.run") as mock_run:
        git_service.delete_branch("branch-name", force=False)

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert cmd == ["git", "branch", "-d", "branch-name"]


def test_delete_branch_force(git_service):
    """Test force deleting a branch."""
    with patch("subprocess.run") as mock_run:
        git_service.delete_branch("branch-name", force=True)

        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        assert cmd == ["git", "branch", "-D", "branch-name"]


def test_get_worktree_info_valid(git_service, tmp_path):
    """Test getting worktree info for valid worktree."""
    worktree_path = tmp_path / "pr-12345"
    worktree_path.mkdir()

    with patch("subprocess.run") as mock_run:
        # Mock git rev-parse for branch name
        mock_run.side_effect = [
            Mock(stdout="review/pr-12345\n", returncode=0),  # branch name
            Mock(stdout="", returncode=0),  # status (clean)
            Mock(returncode=0),  # upstream exists
            Mock(stdout="0\n", returncode=0),  # unpushed commits
        ]

        worktree = git_service.get_worktree_info(worktree_path)

        assert worktree is not None
        assert worktree.pr_number == 12345
        assert worktree.path == worktree_path
        assert worktree.branch_name == "review/pr-12345"
        assert worktree.has_uncommitted_changes is False
        assert worktree.unpushed_commit_count == 0


def test_get_worktree_info_not_pr_worktree(git_service, tmp_path):
    """Test getting worktree info for non-PR worktree."""
    worktree_path = tmp_path / "not-a-pr"
    worktree_path.mkdir()

    worktree = git_service.get_worktree_info(worktree_path)

    assert worktree is None


def test_get_worktree_info_nonexistent(git_service, tmp_path):
    """Test getting worktree info for nonexistent path."""
    worktree_path = tmp_path / "nonexistent"

    worktree = git_service.get_worktree_info(worktree_path)

    assert worktree is None


def test_get_worktree_info_git_error(git_service, tmp_path):
    """Test getting worktree info when git fails."""
    worktree_path = tmp_path / "pr-12345"
    worktree_path.mkdir()

    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        worktree = git_service.get_worktree_info(worktree_path)

        assert worktree is None


def test_get_repo_from_remote_path_not_git_repo(git_service, tmp_path):
    """Test get_repo_from_remote returns None when path is not a git repo."""
    # tmp_path exists but has no .git directory
    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = git_service.get_repo_from_remote(tmp_path)

        assert result is None


def test_get_repo_from_remote_path_does_not_exist(git_service, tmp_path):
    """Test get_repo_from_remote returns None when path does not exist."""
    nonexistent_path = tmp_path / "nonexistent"
    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = subprocess.CalledProcessError(1, "git")

        result = git_service.get_repo_from_remote(nonexistent_path)

        assert result is None


def test_get_worktree_info_current_branch_detection(git_service, tmp_path):
    """Test that get_worktree_info correctly detects current branch."""
    worktree_path = tmp_path / "pr-999"
    worktree_path.mkdir()

    with patch("subprocess.run") as mock_run:
        mock_run.side_effect = [
            Mock(stdout="review/pr-999\n", returncode=0),  # rev-parse branch name
            Mock(stdout="", returncode=0),  # status (clean)
            Mock(returncode=0),  # upstream exists
            Mock(stdout="5\n", returncode=0),  # unpushed commits
        ]

        worktree = git_service.get_worktree_info(worktree_path)

        assert worktree is not None
        assert worktree.branch_name == "review/pr-999"
        assert worktree.pr_number == 999
