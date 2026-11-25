package main

import (
	"encoding/json"
	"testing"

	"github.com/phillipgreenii/claude-extended-tool-approver/internal/claudecodeadapter"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/cmddesc"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/effectpolicy"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/evalcontract"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/goldencorpus"
	"github.com/phillipgreenii/claude-extended-tool-approver/internal/hookio"
)

// TestEvaluateCorpusPathState_GitFactsDecideVerdict pins, through the real
// engine, that the git path state a row names (tracked, gitignored, a
// non-temp repository) decides the verdict, with no host repository involved.
func TestEvaluateCorpusPathState_GitFactsDecideVerdict(t *testing.T) {
	for _, tc := range []struct {
		name string
		fx   goldencorpus.PathStateFixture
		cmd  string
		want evalcontract.Decision
	}{
		{"tracked credential-format file is readable", goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo", GitTracked: []string{"config/.env.local"}}, "cat config/.env.local", evalcontract.Approve},
		{"untracked credential-format file is rejected", goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo", GitRepo: true}, "cat config/.env.local", evalcontract.Reject},
		{"gitignored credential-format file is rejected", goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo", Gitignored: []string{".env"}}, "cat .env", evalcontract.Reject},
		{".git write in a non-temp repo is rejected", goldencorpus.PathStateFixture{CWD: "/repo", ProjectRoot: "/repo", GitRepo: true}, "echo x > .git/hooks/pre-commit", evalcontract.Reject},
		{".git write in a temp repo is approved", goldencorpus.PathStateFixture{CWD: "/tmp/ceta-fixture-repo", ProjectRoot: "/tmp/ceta-fixture-repo", GitRepo: true}, "echo x > .git/hooks/pre-commit", evalcontract.Approve},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree, err := tc.fx.MaterializeTree()
			if err != nil {
				t.Fatal(err)
			}
			defer tree.Cleanup()
			done := tree.Scope()
			defer done()
			raw, err := json.Marshal(hookio.BashToolInput{Command: tree.Expand(tc.cmd)})
			if err != nil {
				t.Fatal(err)
			}
			resp, err := claudecodeadapter.Evaluate(&hookio.HookInput{CWD: tree.CWD, ToolName: "Bash", ToolInput: raw},
				claudecodeadapter.RequestConfig{ProjectRoot: tree.ProjectRoot}, cmddesc.DefaultRegistry(), effectpolicy.DefaultPolicies(), effectpolicy.DefaultGraphPolicies())
			if err != nil {
				t.Fatal(err)
			}
			if resp.Decision != tc.want {
				t.Errorf("%q = %s (%s), want %s", tc.cmd, resp.Decision, resp.Reason, tc.want)
			}
		})
	}
}
