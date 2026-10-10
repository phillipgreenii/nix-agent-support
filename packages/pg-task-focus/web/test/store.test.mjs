import assert from "node:assert/strict";
import { test } from "node:test";
import { ApiError } from "../assets/api.mjs";
import { UNDO_MS } from "../assets/store.mjs";
import { cycle, dimmed, period, readOnlyStore, state, T0 } from "./fixtures.mjs";
import { harness, networkError, problem, said } from "./harness.mjs";

test("the first read loads the state and the configuration, and a configuration change reloads it", async () => {
  const h = harness();
  await h.start();
  assert.equal(h.model.loaded, true);
  assert.ok(h.model.config.cycles.length > 0);
  assert.equal(h.calls.filter((c) => c.get === "config").length, 1);
  await h.store.refresh();
  assert.equal(h.calls.filter((c) => c.get === "config").length, 1, "an unchanged generation does not read it again");
  h.setState(state({ version: { log_lines: 41, config_generation: 2 } }));
  await h.store.refresh();
  assert.equal(h.calls.filter((c) => c.get === "config").length, 2);
});

test("a refresh that fails says so and keeps the last state", async () => {
  const h = harness();
  await h.start();
  h.api.getState = async () => {
    throw networkError();
  };
  await h.store.refresh();
  assert.ok(h.model.loadError);
  assert.ok(h.model.state, "the last state is still shown");
  h.api.getState = async () => state();
  await h.store.refresh();
  assert.equal(h.model.loadError, null);
});

test("overlapping refreshes coalesce into one more read", async () => {
  const h = harness();
  await h.start();
  const before = h.calls.filter((c) => c.get === "state").length;
  await Promise.all([h.store.refresh(), h.store.refresh(), h.store.refresh()]);
  assert.ok(h.calls.filter((c) => c.get === "state").length - before <= 2);
});

// ---- requests ----

test("every mutation carries an idempotency id of the daemon's shape", async () => {
  const h = harness();
  await h.start();
  const t = h.model.state.tasks[1];
  await h.dispatch("task.done", t);
  await h.dispatch("cycleStart.submit");
  for (const id of h.ids()) assert.match(id, /^[0-9A-Za-z]{26}$/);
  assert.equal(new Set(h.ids()).size, h.ids().length, "each request has its own");
});

test("a no-op is a quiet notice and never an error or an Undo", async () => {
  const h = harness({ post: () => ({ changed: false, note: "Review cycle has been paused since 13:00:00Z (09:00 America/New_York)", event_ids: [], version: { log_lines: 40, config_generation: 1 } }) });
  await h.start();
  const r = await h.dispatch("cycle.pause", dimmed());
  assert.equal(r.ok, true);
  assert.equal(h.model.error, null);
  assert.equal(h.model.undo, null);
  assert.match(h.model.notice.text, /has been paused since/);
});

test("a success with an Undo keeps the event just appended, and a batch is undone as a batch", async () => {
  const h = harness({ post: (path) => (path.includes("periods") ? { changed: true, event_ids: ["A", "B", "C"], batch_id: "01JBATCH", version: {} } : { changed: true, event_ids: ["01JE1"], version: {} }) });
  await h.start();
  await h.dispatch("task.done", h.model.state.tasks[1]);
  assert.deepEqual(h.model.undo.ref, { event: "01JE1" });
  assert.equal(h.model.undo.expiresAt, T0 + UNDO_MS);
  await h.store.undo();
  assert.equal(h.lastPost().path ?? h.lastPost().post, "/api/v1/events/01JE1/retract");
  assert.equal(h.model.undo, null);
  assert.match(h.model.notice.text, /^Undone: Completed Post the plan/);

  const periods = (await import("../assets/actions.mjs")).changePeriods({ changes: [] });
  await h.store.perform(periods);
  assert.deepEqual(h.model.undo.ref, { batch: "01JBATCH" });
  await h.store.undo();
  assert.equal(h.lastPost().post, "/api/v1/batches/01JBATCH/retract");
});

test("the Undo lasts ten seconds", async () => {
  const h = harness();
  await h.start();
  await h.dispatch("task.done", h.model.state.tasks[1]);
  assert.ok(h.model.undo);
  h.advance(UNDO_MS - 1);
  assert.ok(h.model.undo);
  h.advance(2);
  assert.equal(h.model.undo, null);
});

test("an Undo the daemon refuses shows the daemon's sentence, naming the dependents", async () => {
  const sentence = "The batch cannot be retracted: event …XYZ completes a task that the batch created.";
  const h = harness({
    post: (path) => {
      if (path.includes("retract")) throw problem("batch_has_dependents", sentence, { details: { events: ["01JXYZ"] } });
      return { changed: true, event_ids: ["A"], batch_id: "01JB", version: {} };
    },
  });
  await h.start();
  await h.store.perform((await import("../assets/actions.mjs")).changePeriods({ changes: [] }));
  await h.store.undo();
  assert.equal(h.model.error.detail, sentence);
  assert.equal(h.model.error.reason, "batch_has_dependents");
  assert.deepEqual(h.model.error.details.events, ["01JXYZ"]);
  assert.ok(h.page().has(sentence));
});

test("a refusal is shown in the daemon's words, unchanged, with its code and trace id", async () => {
  const detail = "Plan the day is already completed by event …AAAAAAAA at 2026-10-07T13:00:00Z (09:00 America/New_York).";
  const h = harness({
    post: () => {
      throw problem("task_already_resolved", detail);
    },
  });
  await h.start();
  const r = await h.dispatch("task.done", h.model.state.tasks[0]);
  assert.equal(r.ok, false);
  assert.equal(h.model.error.detail, detail);
  assert.equal(h.model.error.reason, "task_already_resolved");
  assert.equal(h.model.error.traceId, "trace1");
  assert.equal(h.model.error.retryable, false);
  assert.ok(h.page().has(detail));
  await h.store.retry();
  assert.equal(h.posts().length, 1, "a refusal is not repeated");
});

test("an unreachable daemon leaves the outcome unknown, and a repeat sends the same id", async () => {
  let fail = true;
  const h = harness({
    post: () => {
      if (fail) throw networkError();
      return { changed: true, event_ids: ["01JE"], version: {} };
    },
  });
  await h.start();
  await h.dispatch("task.done", h.model.state.tasks[0]);
  assert.equal(h.model.error.retryable, true);
  const first = h.lastPost().body.id;
  fail = false;
  const r = await h.store.retry();
  assert.equal(r.ok, true);
  assert.equal(h.lastPost().body.id, first, "the same request id, so the daemon applies it at most once");
  assert.equal(h.model.error, null);
});

test("store_unavailable with the outcome unknown is repeatable, with read-only it is not", async () => {
  const unknown = problem("store_unavailable", "The append failed and was rolled back; the outcome is unknown.", { status: 503, details: { store: { state: "ok" } }, store: { state: "ok" } });
  const ro = problem("store_unavailable", "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover", { status: 503, store: readOnlyStore });
  for (const [err, retryable, readOnly] of [[unknown, true, false], [ro, false, true]]) {
    const h = harness({
      post: () => {
        throw err;
      },
    });
    await h.start();
    if (readOnly) h.setState(state({ store: readOnlyStore }));
    await h.dispatch("task.done", h.model.state.tasks[0]);
    assert.equal(h.model.error.retryable, retryable);
    assert.equal(h.model.error.readOnly, readOnly);
    if (readOnly) assert.equal(h.model.state.store.state, "read_only", "the page read the state again and shows the mode");
  }
});

test("a cycle verb the daemon cannot place is a choice: the page repeats it with the cycle the operator picked", async () => {
  const cands = [{ id: "A", title: "Deep work cycle" }, { id: "B", title: "Review cycle" }];
  const h = harness({
    post: (path, body) => {
      if (!body.cycle_id) throw problem("cycle_ambiguous", "Two cycles could be meant.", { status: 400, details: { cycles: cands } });
      return { changed: true, event_ids: ["E"], version: {} };
    },
  });
  await h.start();
  await h.store.perform({ path: "/api/v1/cycles/pause", body: {}, label: "Pause" });
  assert.deepEqual(h.model.error.candidates, cands);
  await h.store.chooseCycle("B");
  assert.equal(h.lastPost().body.cycle_id, "B");
  assert.equal(h.model.error, null);
});

// ---- announcements ----

test("overtime is announced once, and again after a boost ends it", async () => {
  const h = harness({ state: state({ focus: cycle({ remaining_seconds: 3 }) }) });
  await h.start();
  assert.deepEqual(said(h), []);
  h.advance(2000);
  assert.deepEqual(said(h), []);
  h.advance(2000);
  assert.match(said(h).at(-1), /Deep work cycle is out of time/);
  const seq = h.model.announce.polite.seq;
  h.advance(10000);
  assert.equal(h.model.announce.polite.seq, seq, "not every second");
  h.setState(state({ focus: cycle({ remaining_seconds: 300, boost_minutes: 10 }) }));
  await h.store.refresh();
  assert.equal(h.model.announce.polite.seq, seq, "a boost that ends overtime says nothing");
  h.advance(301000);
  assert.ok(h.model.announce.polite.seq > seq, "entering overtime again is announced again");
});

test("two things said in one moment are both announced, and an ended period is announced once", async () => {
  const h = harness({ state: state({ focus: cycle({ remaining_seconds: -10, overtime: true }) }) });
  await h.start();
  assert.equal(said(h).length, 1, "the overtime");
  h.setState(state({ focus: cycle({ remaining_seconds: -10, overtime: true }), periods: [period("day", { ended: true, banner: "New day: roll over", today: "2026-10-08" }), period("week"), period("sprint")], version: { log_lines: 41, config_generation: 1 } }));
  await h.store.refresh();
  assert.deepEqual(said(h).slice(-1), ["New day: roll over"]);
  for (let i = 0; i < 6; i++) h.store.announce(`m${i}`);
  assert.equal(said(h).length, 4, "only the last few are kept");
  assert.equal(h.page().byAttr("id", "live").length, 1);
});

test("an ended period is announced once, and read-only mode is the banner's alert", async () => {
  const h = harness();
  await h.start();
  h.setState(state({ periods: [period("day", { ended: true, banner: "New day: roll over", today: "2026-10-08" }), period("week"), period("sprint")] }));
  await h.store.refresh();
  assert.deepEqual(said(h), ["New day: roll over"]);
  const seq = h.model.announce.polite.seq;
  await h.store.refresh();
  assert.equal(h.model.announce.polite.seq, seq);
  h.setState(state({ store: readOnlyStore }));
  await h.store.refresh();
  assert.equal(h.page().byAttr("data-banner", "read-only")[0].props.role, "alert");
});

test("losing and regaining the stream is said", async () => {
  const h = harness();
  await h.start();
  h.store.setConn("live");
  h.store.setConn("lost");
  assert.match(said(h).at(-1), /connection to the service was lost/);
  h.store.setConn("live");
  assert.match(said(h).at(-1), /is back/);
});

test("the ApiError of a problem keeps what the page shows", () => {
  const e = problem("cycle_stopped", "Deep work cycle is stopped.");
  assert.ok(e instanceof ApiError);
  assert.equal(e.message, "Deep work cycle is stopped.");
  assert.equal(e.outcomeUnknown, false);
});
