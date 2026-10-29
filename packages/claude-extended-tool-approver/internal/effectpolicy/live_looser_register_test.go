package effectpolicy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// knownLiveLooser is the REGISTER of every agreement-table row where the LIVE
// engine approves a command the spike does not (class spike-stricter with a
// live Approve). It mirrors knownSpikeLooser, which covers the opposite
// direction: there the spike is the over-approving engine, here production is.
//
// Why it exists (deep review 2026-10-24, finding agent-support-A F-09):
// TestAgreement used to assert only that the spike is never looser than live.
// A live-Approve/spike-refuse row was accepted silently, so the golden file
// held 133 recorded live over-approvals (git push --exec, sed 'e ls',
// awk system(), xargs sh -c, nix build github:...) that nothing tracked and
// nothing ratcheted. TestAgreement now fails when
//
//   - a live-Approve/spike-not-Approve row is not listed here (a NEW row must be
//     triaged into a class, or into a new class with its own reference), and
//   - a listed row no longer is one (it was fixed or removed: delete the entry,
//     which is the ratchet).
//
// Entries are CLASSES, not rows: each class names one defect bead or one
// ruling in Ref and lists the case names it covers, so the register does not
// carry one bead id per row. A class is NOT a suppression and it does not say
// the live verdict is right: it says the divergence is known and where it is
// tracked. The class table in testdata/agreement.txt is unchanged.
//
// Ref MUST be a bead id (pg2-...) or start with "ruling:". A class whose cause
// is an open operator decision cites the bead that holds the decision.
type liveLooserClass struct {
	// Name is the short class name used in failure messages.
	Name string
	// Ref is the defect bead id(s) or the ruling that tracks the class.
	Ref string
	// Cause says why the live engine approves what the spike refuses.
	Cause string
	// Cases are the agreementCase names (first column of testdata/agreement.txt).
	Cases []string
}

var knownLiveLooser = []liveLooserClass{
	{
		Name: "xargs",
		Ref:  "pg2-imjrv",
		Cause: "safecmds approves xargs by judging the inner command it believes it parsed; the spike " +
			"abstains or rejects where an unknown value flag or a substituted replacement string could " +
			"change what runs.",
		Cases: []string{
			"xargs_rm_f",
			"xargs_child_stdout_pipe_tee",
			"xargs_sh_c_echo",
		},
	},
	{
		Name: "env-and-assignment",
		Ref:  "pg2-6c2rm, pg2-nd6i3",
		Cause: "live approves leaves whose meaning depends on process or shell state set by an " +
			"assignment, export, env-prefix or read (PATH, IFS, GIT_DIR, GIT_INDEX_FILE, dynamic names); the " +
			"spike abstains on unmodeled state changes and rejects the git-dir redirections.",
		Cases: []string{
			"prefix_assign_cmdsubst_rm_abstains",
			"assign_persistent_ifs_abstains",
			"assign_persistent_git_dir_rejects",
			"read_path_abstains",
			"printf_v_var",
			"bracket_v_abstains",
			"test_two_adjacent_live_operands",
			"export_f_unmodeled",
			"export_n_foo",
			"export_path_extend",
			"export_path_extend_substitution_component",
			"export_dynamic_name",
			"home_prefix_cat_readme",
			"git_dir_prefix_git_status",
			"git_index_file_prefix_git_status",
			"git_log_assigned_option_then_live_positional",
			"git_log_export_option_then_live_positional",
		},
	},
	{
		Name: "git-argv-and-state",
		Ref:  "pg2-x5lf6",
		Cause: "the git rule approves argv spellings of exec/write sinks and mutating verbs " +
			"(push remotes and refspecs, worktree remove/move, config writes, --no-verify, unknown flags, " +
			"live positionals, rm/mv into the nix store) that the spike treats as insufficient or forbidden.",
		Cases: []string{
			"git_push_path_remote_abstains",
			"git_push_all_prune_rejects",
			"git_push_glob_refspec_abstains",
			"git_push_lease_with_value_abstains",
			"git_push_prune_rejects",
			"git_push_dynamic_remote_abstains",
			"git_push_no_verify",
			"git_log_unknown_flag",
			"git_log_live_positional",
			"git_log_live_positional_braced",
			"git_log_live_positional_quoted",
			"git_log_live_positional_cmdsubst",
			"git_rev_parse_unknown_flag",
			"git_rev_list_unknown_flag",
			"git_show_unknown_flag",
			"git_branch_foo",
			"git_worktree_remove_force_dirty",
			"git_worktree_remove_force_ignored_only",
			"git_worktree_remove_force_notaworktree",
			"git_worktree_add_dynamic",
			"git_worktree_move_dirty",
			"git_worktree_frobnicate",
			"git_config_user_name_x",
			"git_config_bare_key_value_pattern_write",
			"git_config_unset_key",
			"git_config_add_key_value",
			"git_config_global_key",
			"git_config_edit",
			"git_add_unknown_flag",
			"git_commit_no_verify",
			"git_rm_nix_store",
			"git_mv_readme_nix_store",
			"git_mv_too_few",
		},
	},
	{
		Name: "nix",
		Ref:  "pg2-3t2wm",
		Cause: "the nix rule approves subcommands and flakerefs the spike does not model " +
			"(develop, remote flakerefs, --impure, --commit-lock-file); the global-option misparse is the " +
			"tracked defect, the approval policy for the rest is an open decision on the same finding.",
		Cases: []string{
			"nix_develop_unmodeled",
			"nix_build_remote_flakeref_abstains",
			"nix_build_impure_abstains",
			"nix_flake_update_commit_lock_file_abstains",
		},
	},
	{
		Name: "awk-sed-exec",
		Ref:  "pg2-cbr4dw",
		Cause: "safecmds approves awk/sed/yq/sort programs without parsing the program text, so a " +
			"program that runs a command or writes a file is approved; the spike's dialect parsers refuse " +
			"or abstain. Deep review 2026-10-24 finding agent-support-A F-03 left the fix design (port the " +
			"dialect parsers or use a heuristic) as an operator decision; pg2-cbr4dw holds this register.",
		Cases: []string{
			"sed_w_nix_store",
			"sed_subst_w_nix_store",
			"sed_e_exec",
			"sort_o_nix_store",
			"awk_print_redirect_nix_store",
			"awk_print_append_field_target",
			"awk_system_field",
			"awk_at_load",
			"awk_two_way_pipe",
			"yq_split",
			"yq_system_operator",
			"datachan_yq_load_secret",
		},
	},
	{
		Name: "path-effects",
		Ref:  "pg2-cbr4dw",
		Cause: "live approves rm, cp, redirects, secret-directory reads and go output flags on a " +
			"coarser path model than the spike's delete/write/read ladders (tracked git dirs, dirty " +
			"worktrees, nix store, untracked secrets). Deep review 2026-10-24 finding agent-support-A F-04 " +
			"left which verbs stay approved as an operator decision; pg2-cbr4dw holds this register.",
		Cases: []string{
			"rm_readme",
			"rm_end_of_options",
			"rm_rf_sub_not_ignored",
			"rm_rf_gradle_src",
			"rm_rf_dot_git",
			"rm_rf_worktree_dirty",
			"rm_rf_worktree_ignored_only",
			"rm_rf_worktree_notaworktree",
			"rm_rf_workforests_dirty_repo",
			"rm_rf_workforests_dirty",
			"rm_rf_workforests_mixed",
			"cp_too_few",
			"echo_redirect_untracked_secrets_dir",
			"cat_untracked_secrets_dir_file",
			"grep_untracked_secrets_dir",
			"go_test_coverprofile_nix_store",
			"go_build_o_nix_store",
		},
	},
	{
		Name: "spike-model-gap",
		Ref:  "ruling: the spike-stricter class in classify()'s doc comment (agreement_integration_test.go): the spike abstaining on what live approves is the safe direction, and the live verdict is the authority for flags, verbs and contexts the spike has no model of",
		Cause: "the spike has no schema for the flag, verb, context or configuration (unknown flags, bd verbs, " +
			"kubectl contexts, go run/install/get, cd with a dynamic or nix-store target, bash -n) and " +
			"abstains; live approves them by design.",
		Cases: []string{
			"cat_unknown_flag",
			"ls_unknown_flag",
			"wc_unknown_flag",
			"tail_unknown_flag",
			"head_numeric_shorthand_project",
			"find_frobnicate",
			"which_read_alias",
			"pgrep_signal",
			"jq_arg_missing_value",
			"cd_dynamic_then_cat",
			"cd_dash_then_cat",
			"cd_old_new_form",
			"cd_nix_store_go_test_abstains",
			"cd_nix_store_pn_build_abstains",
			"cd_nix_store_semicolon_go_test_abstains",
			"cd_nix_store_pipeline_go_test_abstains",
			"cd_dynamic_go_test_abstains",
			"bash_n_script",
			"sh_n_script",
			"curl_unvetted",
			"curl_k_vetted",
			"kubectl_no_context_get",
			"kubectl_unlisted_context_get",
			"kubectl_server_flag_no_context",
			"bd_delete_stays_consent",
			"bd_reopen_stays_consent",
			"bd_dep_remove_stays_consent",
			"bd_human_dismiss_stays_consent",
			"bd_dolt_commit",
			"bd_dolt_start",
			"bd_dolt_stop",
			"bd_dolt_killall",
			"bd_dolt_start_reject_configured",
			"bd_dolt_stop_reject_configured",
			"bd_dolt_killall_reject_configured",
			"bd_unknown_verb",
			"bd_C_dir_dynamic_file_flag",
			"go_test_exec_frobnicate",
			"go_run_cmd_tool",
			"go_run_main_flag",
			"go_install_cmd_x",
			"go_get_example_m",
			"go_frobnicate",
		},
	},
}

var liveLooserBeadRE = regexp.MustCompile(`\bpg2-[a-z0-9]+\b`)

// validateLiveLooserRegister returns one message per structural defect of the
// register: a class without a name, cause, cases or a usable Ref, or a case
// listed twice (within or across classes).
func validateLiveLooserRegister(classes []liveLooserClass) []string {
	var problems []string
	seenClass := map[string]bool{}
	owner := map[string]string{}
	for _, c := range classes {
		switch {
		case c.Name == "":
			problems = append(problems, "a class has no Name")
			continue
		case seenClass[c.Name]:
			problems = append(problems, fmt.Sprintf("class %q is declared twice", c.Name))
		}
		seenClass[c.Name] = true
		if !liveLooserBeadRE.MatchString(c.Ref) && !strings.HasPrefix(c.Ref, "ruling: ") {
			problems = append(problems, fmt.Sprintf("class %q: Ref %q is neither a bead id (pg2-...) nor a \"ruling: ...\" citation", c.Name, c.Ref))
		}
		if strings.TrimSpace(c.Cause) == "" {
			problems = append(problems, fmt.Sprintf("class %q has no Cause", c.Name))
		}
		if len(c.Cases) == 0 {
			problems = append(problems, fmt.Sprintf("class %q lists no cases", c.Name))
		}
		for _, name := range c.Cases {
			if prev, dup := owner[name]; dup {
				problems = append(problems, fmt.Sprintf("case %q is listed in class %q and in class %q", name, prev, c.Name))
				continue
			}
			owner[name] = c.Name
		}
	}
	return problems
}

// checkLiveLooserRegister compares the register against the rows the harness
// observed as live-Approve/spike-not-Approve and returns one message per
// mismatch: an observed row no class lists (a new live over-approval, triage
// it) and a listed case that was not observed (fixed or removed, delete it).
func checkLiveLooserRegister(classes []liveLooserClass, observed []string) []string {
	listed := map[string]string{}
	for _, c := range classes {
		for _, name := range c.Cases {
			listed[name] = c.Name
		}
	}
	seen := map[string]bool{}
	var problems []string
	for _, name := range observed {
		seen[name] = true
		if _, ok := listed[name]; !ok {
			problems = append(problems, fmt.Sprintf("UNREGISTERED live-looser row: case %s is live-approve/spike-not-approve and is in no knownLiveLooser class; add it to a class (with a bead or ruling) or fix the live rule", name))
		}
	}
	for name, class := range listed {
		if !seen[name] {
			problems = append(problems, fmt.Sprintf("STALE knownLiveLooser entry: case %s (class %s) is no longer live-approve/spike-not-approve; delete it from the register", name, class))
		}
	}
	sort.Strings(problems)
	return problems
}

// TestKnownLiveLooser_RegisterShape pins the register's structure so a class
// cannot lose its reference or a case be double-listed.
func TestKnownLiveLooser_RegisterShape(t *testing.T) {
	for _, p := range validateLiveLooserRegister(knownLiveLooser) {
		t.Error(p)
	}
}

// TestValidateLiveLooserRegister_RejectsMalformed is the negative control for
// the shape check, over hand-built registers.
func TestValidateLiveLooserRegister_RejectsMalformed(t *testing.T) {
	good := liveLooserClass{Name: "a", Ref: "pg2-abc", Cause: "c", Cases: []string{"x"}}
	tests := []struct {
		name    string
		classes []liveLooserClass
		want    string
	}{
		{"ok", []liveLooserClass{good}, ""},
		{"ruling ref ok", []liveLooserClass{{Name: "a", Ref: "ruling: some ruling", Cause: "c", Cases: []string{"x"}}}, ""},
		{"no bead or ruling", []liveLooserClass{{Name: "a", Ref: "see notes", Cause: "c", Cases: []string{"x"}}}, "neither a bead id"},
		{"no cause", []liveLooserClass{{Name: "a", Ref: "pg2-abc", Cases: []string{"x"}}}, "no Cause"},
		{"no cases", []liveLooserClass{{Name: "a", Ref: "pg2-abc", Cause: "c"}}, "lists no cases"},
		{"duplicate across classes", []liveLooserClass{good, {Name: "b", Ref: "pg2-abc", Cause: "c", Cases: []string{"x"}}}, "is listed in class"},
		{"duplicate class", []liveLooserClass{good, good}, "declared twice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(validateLiveLooserRegister(tc.classes), "\n")
			if tc.want == "" {
				if got != "" {
					t.Fatalf("expected no problems, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("expected a problem containing %q, got %q", tc.want, got)
			}
		})
	}
}

// TestCheckLiveLooserRegister_FlagsNewAndStaleRows is the negative control for
// the ratchet itself: a new live-looser row fails, a fixed row fails, an exact
// match passes.
func TestCheckLiveLooserRegister_FlagsNewAndStaleRows(t *testing.T) {
	classes := []liveLooserClass{{Name: "a", Ref: "pg2-abc", Cause: "c", Cases: []string{"old_a", "old_b"}}}

	if got := checkLiveLooserRegister(classes, []string{"old_a", "old_b"}); len(got) != 0 {
		t.Fatalf("exact match must pass, got %v", got)
	}

	got := strings.Join(checkLiveLooserRegister(classes, []string{"old_a", "old_b", "brand_new"}), "\n")
	if !strings.Contains(got, "UNREGISTERED live-looser row: case brand_new") {
		t.Fatalf("a new live-looser row must fail, got %q", got)
	}

	got = strings.Join(checkLiveLooserRegister(classes, []string{"old_a"}), "\n")
	if !strings.Contains(got, "STALE knownLiveLooser entry: case old_b") {
		t.Fatalf("a fixed row must be reported stale, got %q", got)
	}
}
