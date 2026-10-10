// fanout.go: the one bounded-parallel fan-out helper every umbrella fan-out
// runs its per-backend calls through (bead pg2-55k6y, ADR 0062 "Fan-outs run
// in parallel").
//
// Why. A fan-out used to call its backends one after another, so one wedged
// or slow backend added its whole per-op deadline to every call that included
// it and delayed every backend queued behind it. Operator ruling (Phillip,
// 2026-10-09): fan-outs MUST run in parallel so that no backend being down or
// slow holds up the others; OUTPUT ORDER stays deterministic and unchanged.
//
// Contract of fanOutEach:
//   - fn runs once per backend, concurrently, on at most
//     resolveFanOutConcurrency(reg) goroutines at a time.
//   - The returned slice is index-aligned with backends, so a caller that
//     folds it front to back emits rows in REGISTRATION order regardless of
//     completion order. Callers fold SERIALLY on their own goroutine: only
//     the backend CALL is concurrent, never the construction of the shared
//     outcome, and never a cache write, so no umbrella writer (entity cache,
//     ledger, freshness stamp) is ever entered by two goroutines at once.
//   - Each call keeps its own per-backend deadline (scriptout applies it per
//     exec); the helper adds none, so a hung backend still costs only its own
//     deadline, and the fan-out's wall clock is the SLOWEST backend instead of
//     the SUM.
//   - A panic in fn is recovered into that backend's Err (the caller turns it
//     into the same per-backend degraded row any failing backend yields), so
//     a faulty call never aborts or hangs its siblings.
//   - If ctx is already done when a backend's turn comes, fn is not started
//     and that backend's Err is ctx.Err().
//   - Every goroutine has exited when fanOutEach returns (no leak).
//   - With a single backend, or a cap of 1, fn runs inline on the caller's
//     goroutine, in order: single-backend behavior is unchanged.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-connector/pkg/scriptout"
)

// defaultFanOutConcurrency is the cap on simultaneously running backend calls
// when state.fanout_concurrency is absent or unusable. Decision (bead
// pg2-55k6y): 8. A host registers well under that many backends today (two
// beads trackers, jira, pr-github, slack, calendar, mail, alert, activity
// sources), so the default means "all at once" in practice while still
// bounding the exec fan-out (one subprocess per call) on an unusually large
// registry. It is a cap on subprocesses, not a rate limit: each backend
// still receives exactly one call per fan-out, as before.
const defaultFanOutConcurrency = 8

// fanOutConcurrencyStateKey is the state.<key> a host sets to change the cap
// (a positive integer). 1 restores strictly serial fan-outs, the escape hatch
// should a backend ever prove unsafe to overlap with another.
const fanOutConcurrencyStateKey = "fanout_concurrency"

// resolveFanOutConcurrency reads state.fanout_concurrency, answering
// defaultFanOutConcurrency when it is absent, non-numeric, or below 1.
// reg may be nil (state absent).
func resolveFanOutConcurrency(reg *Registry) int {
	v, ok := reg.StateValue(fanOutConcurrencyStateKey)
	if !ok {
		return defaultFanOutConcurrency
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return defaultFanOutConcurrency
	}
	return n
}

// fanOutResult is one backend's slot: the value fn returned, or the error
// that replaced it (a recovered panic, or ctx ending before the backend's
// turn).
type fanOutResult[R any] struct {
	Val R
	Err error
}

// fanOutEach runs fn for every backend with bounded concurrency and returns
// the results in backends' order. See this file's header for the contract.
func fanOutEach[R any](ctx context.Context, reg *Registry, backends []string, fn func(ctx context.Context, backend string) R) []fanOutResult[R] {
	results := make([]fanOutResult[R], len(backends))
	run := func(i int) {
		if err := ctx.Err(); err != nil {
			results[i].Err = err
			return
		}
		defer func() {
			if p := recover(); p != nil {
				results[i].Err = fmt.Errorf("pg-connector: backend %q call panicked: %v", backends[i], p)
			}
		}()
		results[i].Val = fn(ctx, backends[i])
	}

	workers := min(resolveFanOutConcurrency(reg), len(backends))
	if workers <= 1 {
		for i := range backends {
			run(i)
		}
		return results
	}

	// Indices are handed out in registration order, so with a cap below the
	// backend count the earliest-registered backends start first.
	next := make(chan int)
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range next {
				run(i)
			}
		}()
	}
	for i := range backends {
		next <- i
	}
	close(next)
	wg.Wait()
	return results
}

// backendCall is one backend's outcome of the invoke phase of a fan-out:
// cfgErr is a failure to resolve its config (no call was made), otherwise
// resp/err are reg.Invoke's answer (err also carries a recovered panic or a
// ctx that ended before the backend's turn).
type backendCall struct {
	resp   *scriptout.Response
	err    error
	cfgErr error
}

// invokeAll is the shared invoke phase of the "resolve config, then Invoke"
// fan-outs. configFor resolves one backend's per-call config and runs
// SERIALLY, in registration order, before any call starts (it reads the
// shared parsed registry, which therefore never sees concurrent callers);
// only reg.Invoke itself runs concurrently. The result is index-aligned with
// backends. A backend whose config fails is reported in cfgErr and not
// invoked, exactly as the serial loops did.
func invokeAll(ctx context.Context, reg *Registry, backends []string, op string, args any, configFor func(backend string) (json.RawMessage, error)) []backendCall {
	type prepared struct {
		config json.RawMessage
		err    error
	}
	prep := make(map[string]prepared, len(backends))
	for _, b := range backends {
		config, err := configFor(b)
		prep[b] = prepared{config: config, err: err}
	}
	results := fanOutEach(ctx, reg, backends, func(ctx context.Context, b string) backendCall {
		if p := prep[b]; p.err != nil {
			return backendCall{cfgErr: p.err}
		}
		return invokeCall(ctx, reg, b, op, args, prep[b].config)
	})
	calls := make([]backendCall, len(backends))
	for i, r := range results {
		if r.Err != nil {
			calls[i] = backendCall{err: r.Err}
			if prep[backends[i]].err != nil {
				calls[i] = backendCall{cfgErr: prep[backends[i]].err}
			}
			continue
		}
		calls[i] = r.Val
	}
	return calls
}

func invokeCall(ctx context.Context, reg *Registry, b, op string, args any, config json.RawMessage) backendCall {
	resp, err := reg.Invoke(ctx, b, op, args, config)
	return backendCall{resp: resp, err: err}
}

// registryConfig is the configFor of the fan-outs that attach only the
// backend's static backends.<name> block.
func registryConfig(reg *Registry) func(string) (json.RawMessage, error) {
	return reg.BackendConfig
}

// sourceRows turns a fan-out whose per-backend value is already its sources[]
// row into the rows, in registration order; a backend whose call panicked (or
// whose turn never came because ctx ended) becomes a degraded row carrying
// that error, so it never aborts or silently drops out of the envelope.
func sourceRows(backends []string, results []fanOutResult[SourceResult]) []SourceResult {
	rows := make([]SourceResult, len(results))
	for i, r := range results {
		if r.Err != nil {
			rows[i] = SourceResult{Source: backends[i], Status: SourceDegraded, Reason: r.Err.Error()}
			continue
		}
		rows[i] = r.Val
	}
	return rows
}

// capabilitiesAll probes every backend's capabilities in parallel and returns
// the responses in registration order; a backend whose probe failed (or
// panicked) yields nil, because every caller treats a failed probe as "this
// backend contributes nothing".
func capabilitiesAll(ctx context.Context, reg *Registry, backends []string) []*scriptout.CapabilitiesResponse {
	probes := fanOutEach(ctx, reg, backends, func(ctx context.Context, b string) *scriptout.CapabilitiesResponse {
		resp, err := reg.InvokeCapabilities(ctx, b)
		if err != nil {
			return nil
		}
		return resp
	})
	out := make([]*scriptout.CapabilitiesResponse, len(probes))
	for i, p := range probes {
		if p.Err == nil {
			out[i] = p.Val
		}
	}
	return out
}
