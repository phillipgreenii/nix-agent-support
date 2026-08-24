package main

import (
	"context"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/api"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-pr/pkg/provider/vcs"
)

// fakeVCS satisfies vcs.Provider with controllable GetPR output.
type fakeVCS struct {
	pr  *api.PR
	err error
}

func (f *fakeVCS) GetPR(_ context.Context, repo string, n int) (*api.PR, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := *f.pr
	out.Repo = repo
	out.Number = n
	return &out, nil
}

func (f *fakeVCS) ListMyPRs(context.Context, string) ([]api.PR, error) { return nil, nil }

func (f *fakeVCS) ListTeamPRs(context.Context, string, []string) ([]api.PR, error) { return nil, nil }

func (f *fakeVCS) CreatePR(context.Context, string, bool, string, string, string, string, []string, []string) (*api.PR, error) {
	return nil, nil
}
func (f *fakeVCS) UpdatePR(context.Context, string, int, string) error   { return nil }
func (f *fakeVCS) SetDraft(context.Context, string, int, bool) error     { return nil }
func (f *fakeVCS) SetAutomerge(context.Context, string, int, bool) error { return nil }
func (f *fakeVCS) Merge(context.Context, string, int) error              { return nil }
func (f *fakeVCS) Close(context.Context, string, int) error              { return nil }
func (f *fakeVCS) ListComments(context.Context, string, int) ([]api.Comment, error) {
	return nil, nil
}

func (f *fakeVCS) AddComment(context.Context, string, int, string) (*api.Comment, error) {
	return nil, nil
}

func (f *fakeVCS) ReplyToThread(context.Context, string, string, string) (*api.Comment, error) {
	return nil, nil
}
func (f *fakeVCS) ResolveThread(context.Context, string, string) error { return nil }
func (f *fakeVCS) PostReview(context.Context, string, int, string, string, []api.Comment) (*api.Review, error) {
	return nil, nil
}

func (f *fakeVCS) ListReviews(context.Context, string, int) ([]api.Review, error) {
	return nil, nil
}

// Compile check.
var _ vcs.Provider = (*fakeVCS)(nil)

// resetPRFlags clears mutable state between cobra tests since flag values
// persist across rootCmd.Execute() calls.
func resetPRFlags() {
	prF = prFlags{}
}

// The five tests that used to live here (TestPRShow_HumanOutput,
// TestPRShow_JSONOutput, TestPRInfo_AliasOfShow, TestPRInfo_ShowPlusEnrichment,
// TestPRInfo_NoStore, TestPRInfo_JSONIsValid, TestPRShow_InvalidNumber,
// TestRenderEnrichment) exercised `pr show` / `pr info` / `renderEnrichment`,
// all of which pg2-4dz88.5.7 removed outright per the operator ruling on
// pg2-4dz88.5.2 ("surviving name is 'view'; retired names show/info/pr-info
// are removed outright with cobra's plain default unknown-command error").
// Their replacement, `pr view`, is covered by cmd/pg-pr/pr_view_test.go,
// including the invalid-number case (TestPRView_InvalidNumber) and the
// retired-name compatibility assertions
// (TestRetiredNames_RemovedOutright_BareUnknownCommand).
