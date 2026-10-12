package entity

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/config"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/cmd/pg-connector-github/internal/ratefeed"
	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/connectord/governor"
)

// Resolver turns owner/repo#n references the store has not seen into node ids.
// A first sighting is resolved with `repository { pullRequest(number:) }`,
// batched as aliased fields in ONE GraphQL document, so any number of unknown
// references (up to BatchSize) costs one request.
type Resolver struct {
	Identity *Identity
	// GH runs `gh <args...>`; *github.CLI satisfies it.
	GH ratefeed.Runner
	// Governor admits each document; Feed books its spend and reading.
	Governor *governor.Governor
	Feed     *ratefeed.Feed
	// BatchSize caps the references in one document; zero means
	// config.DefaultBatchSize.
	BatchSize int
}

// Resolution is the answer for one requested reference.
type Resolution struct {
	// Requested is the reference as the caller wrote it.
	Requested Ref
	// NodeID is the PR's GraphQL node id; empty when Found is false.
	NodeID string
	// Ref is the reference in GitHub's own spelling (the alias table's form).
	Ref Ref
	// Found is false when GitHub answered that no such pull request exists (or
	// it is not visible to the token).
	Found bool
	// FromStore is true when the alias table already knew the reference, so no
	// origin call was made for it.
	FromStore bool
	// Renamed is true when GitHub returned a node id the table already had under
	// a different reference; OldRef is that earlier reference. The alias has been
	// updated, and the caller is told so it can log the rename.
	Renamed bool
	OldRef  Ref
}

// Resolve resolves refs, in order. References the alias table knows make no
// origin call; the rest go out in one document per BatchSize references, each
// admitted by the governor as call.Class (call.Purpose is what the budget row
// records; its Group and BatchSize are filled in here). The returned cost is the sum of the
// documents' own rateLimit cost. On failure it returns what was resolved before
// the failing document (the rest of the slice has Found false and FromStore
// false) with the error.
func (r *Resolver) Resolve(ctx context.Context, call ratefeed.Meta, refs []Ref) ([]Resolution, int64, error) {
	out := make([]Resolution, len(refs))
	var unknown []Ref        // distinct by Key, in first-seen order
	at := map[string][]int{} // Key -> indexes into out
	for i, ref := range refs {
		out[i] = Resolution{Requested: ref}
		nodeID, canonical, ok, err := r.Identity.NodeID(ctx, ref)
		if err != nil {
			return out, 0, err
		}
		if ok {
			out[i] = Resolution{Requested: ref, NodeID: nodeID, Ref: canonical, Found: true, FromStore: true}
			continue
		}
		if _, seen := at[ref.Key()]; !seen {
			unknown = append(unknown, ref)
		}
		at[ref.Key()] = append(at[ref.Key()], i)
	}

	size := r.BatchSize
	if size <= 0 {
		size = config.DefaultBatchSize
	}
	var cost int64
	for start := 0; start < len(unknown); start += size {
		end := min(start+size, len(unknown))
		batch := unknown[start:end]
		answers, c, err := r.resolveBatch(ctx, call, batch)
		cost += c
		if err != nil {
			return out, cost, err
		}
		for j, ref := range batch {
			a := answers[j]
			var up Update
			if a.found {
				var perr error
				if up, perr = r.Identity.Put(ctx, a.nodeID, a.ref); perr != nil {
					return out, cost, perr
				}
			}
			for _, i := range at[ref.Key()] {
				out[i] = Resolution{
					Requested: refs[i], NodeID: a.nodeID, Ref: a.ref, Found: a.found,
					Renamed: up.Renamed, OldRef: up.Old,
				}
			}
		}
	}
	return out, cost, nil
}

type answer struct {
	found  bool
	nodeID string
	ref    Ref
}

// resolveDocument builds the one document for n references: a field `p<i>` per
// reference, its owner, name and number passed as variables so no reference text
// is ever spliced into the query.
func resolveDocument(n int) string {
	var decl, body strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			decl.WriteString(", ")
		}
		fmt.Fprintf(&decl, "$o%d: String!, $r%d: String!, $n%d: Int!", i, i, i)
		fmt.Fprintf(&body, "  p%d: repository(owner: $o%d, name: $r%d) {\n    nameWithOwner\n    pullRequest(number: $n%d) { id number }\n  }\n", i, i, i, i)
	}
	return "query(" + decl.String() + ") {\n  rateLimit { cost remaining resetAt }\n" + body.String() + "}\n"
}

type resolveResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Type string `json:"type"`
	} `json:"errors"`
}

type resolveField struct {
	NameWithOwner string `json:"nameWithOwner"`
	PullRequest   *struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
	} `json:"pullRequest"`
}

func (r *Resolver) resolveBatch(ctx context.Context, call ratefeed.Meta, batch []Ref) ([]answer, int64, error) {
	tk, err := r.Governor.Admit(governor.Request{Bucket: governor.BucketGraphQL, Class: call.Class, Cost: 1})
	if err != nil {
		return nil, 0, err
	}
	args := []string{"api", "graphql", "-F", "query=" + resolveDocument(len(batch))}
	for i, ref := range batch {
		args = append(args,
			"-f", fmt.Sprintf("o%d=%s", i, ref.Owner),
			"-f", fmt.Sprintf("r%d=%s", i, ref.Repo),
			"-F", fmt.Sprintf("n%d=%d", i, ref.Number))
	}
	body, runErr := r.GH.Run(ctx, args...)
	call.Group, call.BatchSize = "", len(batch)
	_ = r.Feed.SettleGraphQL(tk, call, body, runErr)
	var cost int64
	if rd, found, perr := ratefeed.ParseGraphQL(body); perr == nil && found {
		cost = rd.Cost
	}

	var resp resolveResponse
	jsonErr := json.Unmarshal(body, &resp)
	if runErr != nil {
		// gh exits non-zero when the response carries GraphQL errors. A missing
		// repository or pull request is an expected answer (a null field with a
		// NOT_FOUND error); anything else is a failure.
		if jsonErr != nil || resp.Data == nil || !onlyNotFound(resp.Errors) {
			return nil, cost, runErr
		}
	} else if jsonErr != nil {
		return nil, cost, fmt.Errorf("entity: parse identity response: %w", jsonErr)
	}

	answers := make([]answer, len(batch))
	for i, ref := range batch {
		raw := resp.Data["p"+strconv.Itoa(i)]
		var f resolveField
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, cost, fmt.Errorf("entity: parse identity field for %s: %w", ref, err)
		}
		if f.PullRequest == nil || f.PullRequest.ID == "" {
			continue
		}
		canonical, err := NewRef(f.NameWithOwner, f.PullRequest.Number)
		if err != nil {
			return nil, cost, fmt.Errorf("entity: GitHub answered %s with a bad reference: %w", ref, err)
		}
		answers[i] = answer{found: true, nodeID: f.PullRequest.ID, ref: canonical}
	}
	return answers, cost, nil
}

func onlyNotFound(errs []struct {
	Type string `json:"type"`
},
) bool {
	if len(errs) == 0 {
		return false
	}
	for _, e := range errs {
		if e.Type != "NOT_FOUND" {
			return false
		}
	}
	return true
}
