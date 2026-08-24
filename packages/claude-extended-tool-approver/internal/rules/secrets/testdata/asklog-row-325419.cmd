bd comment pg2-olt3 "closed by /unblock-human-beads under an explicit operator ruling on bead lifecycle (2026-07-29).

OPERATOR'S WORDS: 'the correct structure of beads for the zr monorepo is that work is done until a PR is created. That initial work should be considered done before the PR is created. There will be a new PR created which represents the PR and it may have beads under it. those beads would be corrections and fixes for the PR, but the original bead is done.'

APPLIED HERE: the implementation IS done -- all 6 design points implemented, merged, PUSHED and LIVE-DEPLOYED, confirmed by the 2026-07-15 status correction and the 2026-07-16 post-apply observation (applied c3e90c8, daemon org.nixos.pg-pr-sync healthy, exit 0). The 2026-07-07 'MERGED to local main, NOT pushed, NOT deployed' comments are STALE and were already corrected. So under the ruling above, this bead is done and should not have stayed open.

WHY IT WAS STUCK, and why no agent could have discharged it: the only remaining item was the 9 deploy-gated e2e checks, which need an ORGANIC non-local-head TEAMMATE PR plus a rotating SSH-cert cycle -- explicitly 'cannot be forced on demand'. That is not apply-waiting (the apply already happened); it is an unschedulable external event. A bead held open on one never converges, and it parks in the human queue indefinitely because drain cannot discharge it either.

THE E2E CHECKLIST IS NOT LOST. It lives in the repo at phillipgreenii-nix-agent-support/.superpowers/sdd/task-9-report.md (9 checks), which is committed and durable independently of this bead. This bead also remains readable in bd after closing. Per the ruling, NO speculative 'go observe this later' follow-up bead was filed -- a new bead gets filed if and when a check actually FAILS, as a correction, not as a reminder to look.

DEFERRED MINORS, unchanged and deliberately not blocking: CLICertChecker's time.Now() is not injectable; FetchOutcome has no String(). File them as their own beads if they ever matter.

EVIDENCE OF THE DELIVERED WORK, for anyone auditing later:
 - pre-fetch credential gate: packages/pg-pr/internal/sync/prefetch.go (PreFetchGate.Ensure, single-flight + cooldown, cert-validity probe via ~/.ssh/agent glob, classifyFetch step tokens)
 - step-cli + rg on the daemon launchd PATH: your-private-flake/darwin/services/pg-pr-sync/default.nix:68,71
 - --model sonnet + --permission-mode bypassPermissions on the review spawn: packages/pg-pr/cmd/pg-pr/reviewspawn.go:68
 - reviewer/orchestrator agent-def hardening (rg/git-grep, STOP-on-failure)
 - impl commits b098471 (agent-support, 9 commits 4aac4bb..b098471) and 245ae04 (the private flake); b098471 is an ancestor of agent-support origin/main
 - gates green at the time: go test 29/29, vet, gofmt, prek 12/12, nix flake check, pn workspace build" --actor "17c968a9-81cc-4ea5-ac6b-90a0b0e6f7be-unblock"
