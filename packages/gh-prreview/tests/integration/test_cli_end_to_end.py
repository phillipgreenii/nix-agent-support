"""End-to-end CLI tests using Click's test runner."""

from click.testing import CliRunner

from gh_prreview.cli.main import cli


class TestCliEndToEnd:
    """End-to-end CLI tests."""

    def test_help_command(self) -> None:
        """CLI should show help."""
        runner = CliRunner()
        result = runner.invoke(cli, ["--help"])

        assert result.exit_code == 0
        assert "GitHub Pull Request Review Extension" in result.output

    def test_version_command(self) -> None:
        """CLI should show version."""
        runner = CliRunner()
        result = runner.invoke(cli, ["--version"])

        assert result.exit_code == 0
        assert "gh-prreview" in result.output

    def test_list_awaiting_help(self) -> None:
        """list-awaiting subcommand should show help."""
        runner = CliRunner()
        result = runner.invoke(cli, ["list-awaiting", "--help"])

        assert result.exit_code == 0
        assert "List PRs awaiting your review" in result.output

    def test_checkout_help(self) -> None:
        """checkout subcommand should show help."""
        runner = CliRunner()
        result = runner.invoke(cli, ["checkout", "--help"])

        assert result.exit_code == 0
        assert "Checkout a PR as a git worktree" in result.output

    def test_list_local_help(self) -> None:
        """list-local subcommand should show help."""
        runner = CliRunner()
        result = runner.invoke(cli, ["list-local", "--help"])

        assert result.exit_code == 0
        assert "List all locally checked out PR worktrees" in result.output

    def test_remove_help(self) -> None:
        """remove subcommand should show help."""
        runner = CliRunner()
        result = runner.invoke(cli, ["remove", "--help"])

        assert result.exit_code == 0
        assert "Remove PR review worktrees" in result.output

    def test_remove_without_args_shows_error(self, monkeypatch, tmp_path) -> None:
        """remove without arguments or --closed should show error."""
        # Set required config
        monkeypatch.setenv("GH_PRREVIEW_REVIEW_PATH", str(tmp_path))

        runner = CliRunner()
        result = runner.invoke(cli, ["remove"])

        assert result.exit_code == 1
        assert "Error" in result.output
