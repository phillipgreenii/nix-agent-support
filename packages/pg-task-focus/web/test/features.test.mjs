import assert from "node:assert/strict";
import { test } from "node:test";
import { defaultPeriodForm, periodProblems, periodRequest, rolloverBody, rolloverProblems } from "../assets/feature-periods.mjs";
import { reasonsFrom } from "../assets/feature-tasks.mjs";
import { cycle, dimmed, model, period, state, task, T0 } from "./fixtures.mjs";
import { harness, problem, tick } from "./harness.mjs";

// ---- tasks ----

test("Done sends no time of its own", async () => {
  const h = harness();
  await h.start();
  await h.dispatch("task.done", h.model.state.tasks[1]);
  const p = h.lastPost();
  assert.equal(p.post, "/api/v1/tasks/day%3A2026-10-07%3Apost-plan/complete");
  assert.equal("effective_at" in p.body, false);
});

test("Done at sends the chosen time, read in the display zone", async () => {
  const h = harness();
  await h.start();
  const t = h.model.state.tasks[1];
  h.dispatch("task.openDoneAt", t);
  assert.equal(h.model.ui.taskForm.at, "2026-10-07T09:00:00", "it defaults to now, in the zone the page names");
  h.dispatch("task.field", "at", "2026-10-07T08:45:00");
  await h.dispatch("task.submit");
  assert.equal(h.lastPost().body.effective_at, "2026-10-07T12:45:00.000Z");
  assert.equal(h.model.ui.taskForm, null, "the form closes on success");
});

test("Done at with something that is not a time is refused in the page, with a sentence", async () => {
  const h = harness();
  await h.start();
  h.dispatch("task.openDoneAt", h.model.state.tasks[1]);
  h.dispatch("task.field", "at", "soon");
  await h.dispatch("task.submit");
  assert.equal(h.posts().length, 0);
  assert.match(h.model.ui.taskForm.problem, /date and time/);
});

test("Skip needs a reason that is not blank, sends it, and keeps the form open when the daemon refuses", async () => {
  const h = harness({
    post: (path, body) => {
      if (body.reason === "x") throw problem("task_already_resolved", "Already done.");
      return { changed: true, event_ids: ["01JE"], version: {} };
    },
  });
  await h.start();
  const t = h.model.state.tasks[1];
  h.dispatch("task.openSkip", t);
  h.dispatch("task.field", "reason", "   ");
  await h.dispatch("task.submit");
  assert.equal(h.posts().length, 0, "a blank reason never leaves the page");
  assert.match(h.model.ui.taskForm.problem, /not blank/);
  h.dispatch("task.field", "reason", "x");
  await h.dispatch("task.submit");
  assert.equal(h.model.error.detail, "Already done.");
  assert.ok(h.model.ui.taskForm, "the form stays so the reason is not lost");
  h.dispatch("task.field", "reason", "  not today ");
  await h.dispatch("task.submit");
  assert.deepEqual(h.lastPost().body.reason, "not today");
  assert.equal(h.model.ui.taskForm, null);
  assert.deepEqual(h.model.undo.label, "Skipped Post the plan for the day");
});

test("the recent skip reasons come from the corrected log, newest first, without repeats or retracted ones", () => {
  const ev = (reason, retracted = false) => ({ retracted, event: { type: "task.skipped", data: { reason } } });
  const items = [ev("old"), ev("busy"), ev("gone", true), ev("busy"), ev("new")];
  assert.deepEqual(reasonsFrom(items), ["new", "busy", "old"]);
  assert.deepEqual(reasonsFrom([{ retracted: false, event: { type: "task.completed", data: {} } }]), []);
});

test("the page offers the recent reasons it read at start", async () => {
  const h = harness({ events: (q) => ({ view: "corrected", events: q.types?.[0] === "task.skipped" ? [{ id: "1", retracted: false, corrected_by: [], event: { type: "task.skipped", data: { reason: "not needed" } } }] : [] }) });
  await h.start();
  assert.deepEqual(h.model.ui.reasons, ["not needed"]);
});

// ---- cycles ----

test("starting a cycle sends the type and the override, and the first type is the default", async () => {
  const h = harness();
  await h.start();
  await h.dispatch("cycleStart.submit");
  assert.deepEqual(h.lastPost().body.type, "notifications");
  assert.equal("minutes" in h.lastPost().body, false);
  h.dispatch("cycleStart.field", "type", "deep-work");
  h.dispatch("cycleStart.field", "minutes", "40");
  await h.dispatch("cycleStart.submit");
  assert.deepEqual([h.lastPost().body.type, h.lastPost().body.minutes], ["deep-work", 40]);
  h.dispatch("cycleStart.field", "minutes", "0");
  const n = h.posts().length;
  await h.dispatch("cycleStart.submit");
  assert.equal(h.posts().length, n, "zero minutes is refused in the page");
  assert.match(h.model.ui.cycleStart.problem, /whole number/);
});

test("every cycle control names its cycle in the request", async () => {
  const h = harness({ state: state({ focus: cycle(), dimmed: [dimmed()] }) });
  await h.start();
  const f = h.model.state.focus;
  const d = h.model.state.dimmed[0];
  await h.dispatch("cycle.pause", f);
  await h.dispatch("cycle.stop", d);
  await h.dispatch("cycle.switch", d);
  await h.dispatch("cycle.boost", f, 10);
  await h.dispatch("cycle.resume", d);
  const bodies = h.posts().map((p) => p.body);
  assert.deepEqual(bodies.map((b) => b.cycle_id ?? b.to), [f.id, d.id, d.id, f.id, d.id]);
  assert.equal(bodies[3].minutes, 10);
});

test("a custom boost must be a whole number of minutes", async () => {
  const h = harness({ state: state({ focus: cycle() }) });
  await h.start();
  h.dispatch("boost.field", cycle().id, "7.5");
  await h.dispatch("boost.custom", cycle());
  assert.equal(h.posts().length, 0);
  assert.match(h.model.announce.assertive.items.at(-1).text, /whole number/);
  h.dispatch("boost.field", cycle().id, "15");
  await h.dispatch("boost.custom", cycle());
  assert.equal(h.lastPost().body.minutes, 15);
  assert.equal(h.model.ui.boost[cycle().id], "");
});

test("saving a note sends the whole form, and a draft survives a refresh until it is saved", async () => {
  const h = harness({ state: state({ focus: cycle({ type: "deep-work", kv: [{ key: "ticket", value: "A-1" }] }) }) });
  await h.start();
  const f = h.model.state.focus;
  h.dispatch("note.field", f, "wrote the thing");
  h.dispatch("note.kv", f, 1, "value", "77");
  h.dispatch("note.add", f);
  h.dispatch("note.kv", f, 2, "key", "extra");
  await h.store.refresh();
  const { draftOf } = await import("../assets/feature-cycles.mjs");
  assert.equal(draftOf(h.model, f).note, "wrote the thing", "a refresh does not discard what was typed");
  await h.dispatch("note.save", f);
  assert.deepEqual(h.lastPost().body, { id: h.lastPost().body.id, cycle_id: f.id, note: "wrote the thing", kv: [{ key: "ticket", value: "A-1" }, { key: "pr", value: "77" }] }, "the empty pair is not sent");
  assert.equal(draftOf(h.model, f).dirty, false, "after saving the page shows the daemon's version again");
});

// ---- periods ----

test("the period form defaults: today in the browser's zone, the previous length for a week and sprint", () => {
  const m = model({ browserZone: "America/Chicago", now: Date.parse("2026-10-12T14:00:00Z"), state: state({ periods: [period("day", { ended: true }), period("week", { ended: true }), period("sprint")] }) });
  const f = defaultPeriodForm(m);
  assert.equal(f.tz, "America/Chicago", "the zone comes from the browser");
  assert.deepEqual(f.kinds.day, { on: true, start: "2026-10-12", end: "", label: "" });
  assert.deepEqual(f.kinds.week, { on: true, start: "2026-10-12", end: "2026-10-18", label: "" }, "start plus the previous week's length of six days");
  assert.equal(f.kinds.sprint.on, false, "only the periods that have ended are selected");
  assert.equal(f.kinds.sprint.end, "2026-10-25", "and the sprint's end is prefilled too, from its length of 13 days");
});

test("with an empty log all three are proposed, the week gets seven days and the sprint's end is left to the operator", () => {
  const m = model({ state: state({ initialized: false, periods: [], tasks: [] }) });
  const f = defaultPeriodForm(m);
  assert.deepEqual(["day", "week", "sprint"].map((k) => f.kinds[k].on), [true, true, true]);
  assert.equal(f.kinds.week.end, "2026-10-13");
  assert.equal(f.kinds.sprint.end, "");
  assert.ok(periodProblems(f).some((p) => /sprint's last day/.test(p)));
});

test("the request: only the chosen kinds, the zone, an end for a week, a backdated time with no lower bound", () => {
  const f = defaultPeriodForm(model({ state: state({ periods: [period("day", { ended: true }), period("week"), period("sprint")] }) }));
  f.kinds.day.label = "  Monday ";
  f.backdate = "1999-12-31T23:59";
  f.profile = "on-call";
  const r = periodRequest(f);
  assert.deepEqual(r.changes, [{ kind: "day", start: "2026-10-07", tz: "America/New_York", label: "Monday" }]);
  assert.equal(r.effective_at, "2000-01-01T04:59:00.000Z", "a time decades back is accepted; the page sets no lower bound");
  assert.equal(r.profile, "on-call");
  assert.deepEqual(periodProblems(f), []);
  f.kinds.day.on = false;
  assert.deepEqual(periodProblems(f), ["Choose at least one period to begin."]);
  f.kinds.week.on = true;
  f.kinds.week.end = "2026-10-01";
  assert.ok(periodProblems(f).some((p) => /cannot end before it starts/.test(p)));
});

const PREVIEW = (over = {}) => ({
  dry_run: true,
  state_version: { log_lines: 40, config_generation: 1 },
  preview: { leaving: [{ id: "day:2026-10-07:a", title: "A", overdue: true }, { id: "day:2026-10-07:b", title: "B", overdue: false }], materialize: [{ id: "day:2026-10-08:a", title: "A", overdue: false }], profile_add: [], profile_withdraw: [], profile_reinstate: [], not_materialized: [], blocking_cycles: [], ...over },
});

function periodHarness(opts = {}) {
  const ended = state({ periods: [period("day", { ended: true, banner: "New day: roll over", today: "2026-10-08" }), period("week"), period("sprint")], ...opts.state });
  return harness({
    state: ended,
    post: (path, body) => {
      if (opts.post) return opts.post(path, body);
      if (body.dry_run) return PREVIEW(opts.preview);
      return { changed: true, event_ids: ["A", "B", "C"], batch_id: body.id, version: {} };
    },
    now: T0,
  });
}

test("the period modal: preview first, then confirm carries the preview's version and the batch id", async () => {
  const h = periodHarness();
  h.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h.start();
  h.dispatch("period.open");
  assert.equal(h.model.ui.modal, "period");
  assert.equal(h.page().button("Confirm the change").props.disabled, true, "nothing is confirmable before the preview");
  await h.dispatch("period.confirm");
  assert.equal(h.posts().length, 0, "nor can it be confirmed without one");
  await h.dispatch("period.preview");
  const preview = h.lastPost();
  assert.equal(preview.body.dry_run, true);
  assert.deepEqual(preview.body.changes, [{ kind: "day", start: "2026-10-08", tz: "America/New_York" }]);
  const p = h.page();
  assert.ok(p.has("Open tasks in the periods being left"));
  assert.ok(p.has("A") && p.has("B"));
  assert.ok(p.has("→ will be marked missed"));
  assert.ok(p.has("Undo is refused once later events depend on the new periods"), "the limit on undo is stated");
  assert.equal(p.button("Confirm the change").props.disabled, false);
  await h.dispatch("period.confirm");
  const confirm = h.lastPost();
  assert.deepEqual(confirm.body.expected_version, { log_lines: 40, config_generation: 1 });
  assert.equal("dry_run" in confirm.body, false);
  assert.ok(confirm.body.id, "the request id is the batch id");
  assert.equal(h.model.ui.modal, null, "the modal closes on success");
  assert.ok(h.model.undo, "and the change can be undone");
});

test("changing the form makes the preview stale: it must be read again", async () => {
  const h = periodHarness();
  h.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h.start();
  h.dispatch("period.open");
  await h.dispatch("period.preview");
  assert.equal(h.page().button("Confirm the change").props.disabled, false);
  h.dispatch("period.field", "kinds.day.start", "2026-10-09");
  assert.equal(h.page().button("Confirm the change").props.disabled, true);
  assert.equal(h.page().has("Open tasks in the periods being left"), false);
});

test("mark all missed, skip all with one reason, and a skip of one task with its own reason", async () => {
  const h = periodHarness();
  h.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h.start();
  h.dispatch("period.open");
  await h.dispatch("period.preview");
  const leaving = h.model.ui.period.preview.result.preview.leaving;

  // Skipping every task needs its one reason.
  h.dispatch("rollover.mode", "skipAll");
  assert.equal(h.page().button("Confirm the change").props.disabled, true);
  assert.deepEqual(rolloverProblems(h.model.ui.period.rollover, leaving), ["Give the one reason every open task is skipped for."]);
  h.dispatch("rollover.reason", "  holiday ");
  h.dispatch("rollover.task", "day:2026-10-07:b", true, undefined);
  h.dispatch("rollover.task", "day:2026-10-07:b", undefined, "its own");
  assert.deepEqual(rolloverBody(h.model.ui.period.rollover, leaving), { skip_all_reason: "holiday", overrides: [{ task_id: "day:2026-10-07:b", reason: "its own" }] });
  await h.dispatch("period.confirm");
  assert.equal(h.lastPost().body.skip_all_reason, "holiday");
  assert.deepEqual(h.lastPost().body.overrides, [{ task_id: "day:2026-10-07:b", reason: "its own" }]);

  // "Mark all missed" is the default and sends neither.
  const h2 = periodHarness();
  h2.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h2.start();
  h2.dispatch("period.open");
  await h2.dispatch("period.preview");
  h2.dispatch("rollover.mode", "missed");
  await h2.dispatch("period.confirm");
  assert.equal("skip_all_reason" in h2.lastPost().body, false);
  assert.equal("overrides" in h2.lastPost().body, false);

  // A skip with no reason cannot be confirmed.
  const h3 = periodHarness();
  h3.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h3.start();
  h3.dispatch("period.open");
  await h3.dispatch("period.preview");
  h3.dispatch("rollover.task", "day:2026-10-07:a", true, undefined);
  assert.equal(h3.page().button("Confirm the change").props.disabled, true);
  await h3.dispatch("period.confirm");
  assert.equal(h3.posts().filter((p) => !p.body.dry_run).length, 0);
  assert.match(h3.model.ui.period.problems[0], /Give a reason for skipping A/);
});

test("a cycle that is running or paused blocks the change, with Stop and End at, and no End at is prefilled", async () => {
  const h = periodHarness({
    state: { focus: cycle(), dimmed: [dimmed()] },
    preview: { blocking_cycles: [{ id: cycle().id, title: "Deep work cycle", status: "running" }, { id: dimmed().id, title: "Review cycle", status: "paused" }] },
  });
  h.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h.start();
  h.dispatch("period.open");
  // Before any preview the state already names the blockers.
  let p = h.page();
  assert.ok(p.has("A cycle is not stopped"));
  assert.ok(p.hasButton("Stop Deep work cycle now") && p.hasButton("Stop Review cycle now"));
  assert.ok(p.hasButton("End Deep work cycle at the time entered"));
  assert.equal(p.control("End Deep work cycle at (America/New_York)").props.value, "", "there is no End at prefill");
  await h.dispatch("period.preview");
  p = h.page();
  assert.equal(p.button("Confirm the change").props.disabled, true, "confirmation is blocked");
  assert.ok(p.has("Open tasks in the periods being left"), "the full preview is still shown");

  // End at needs the operator's time.
  await h.dispatch("blocker.endAt", { id: cycle().id, title: "Deep work cycle" });
  assert.equal(h.posts().filter((x) => x.post.includes("stop")).length, 0);
  assert.match(h.model.ui.period.endAtProblem[cycle().id], /really ended/);
  h.dispatch("blocker.endAtField", cycle().id, "2026-10-07T17:30");
  await h.dispatch("blocker.endAt", { id: cycle().id, title: "Deep work cycle" });
  const stop = h.posts().filter((x) => x.post.includes("/cycles/stop")).at(-1);
  assert.deepEqual([stop.body.cycle_id, stop.body.effective_at], [cycle().id, "2026-10-07T21:30:00.000Z"]);
  assert.equal(h.model.undo, null, "the modal's own stop offers no Undo behind the dialog");
  // Stopping re-reads the preview.
  const previews = h.posts().filter((x) => x.body.dry_run).length;
  assert.ok(previews >= 2);
});

test("the daemon's refusal of the change is shown inside the modal, in its words", async () => {
  const detail = "The periods cannot change while Deep work cycle (…0000001) is running.";
  const h = periodHarness({
    post: (path, body) => {
      if (body.dry_run) return PREVIEW();
      throw problem("cycle_active", detail, { details: { cycles: [{ id: cycle().id, title: "Deep work cycle" }] } });
    },
  });
  h.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h.start();
  h.dispatch("period.open");
  await h.dispatch("period.preview");
  await h.dispatch("period.confirm");
  assert.equal(h.model.error, null, "not in the page's error box, which is behind the dialog");
  assert.equal(h.model.ui.period.error.detail, detail);
  assert.ok(h.page().has(detail));
  assert.equal(h.model.ui.modal, "period", "the modal stays open");
});

test("a stale preview is read again and the operator is asked to look again", async () => {
  let first = true;
  const h = periodHarness({
    post: (path, body) => {
      if (body.dry_run) return PREVIEW();
      if (first) {
        first = false;
        throw problem("stale_preview", "The log changed since the preview.");
      }
      return { changed: true, event_ids: ["A"], version: {} };
    },
  });
  h.clock(Date.parse("2026-10-08T13:00:00Z"));
  await h.start();
  h.dispatch("period.open");
  await h.dispatch("period.preview");
  await h.dispatch("period.confirm");
  assert.equal(h.model.ui.period.stale, true);
  assert.equal(h.posts().filter((p) => p.body.dry_run).length, 2, "the preview was read again");
  assert.ok(h.page().has("the preview was read again"));
  assert.equal(h.model.ui.modal, "period");
  await h.dispatch("period.confirm");
  assert.equal(h.model.ui.modal, null);
});

test("the profile modal previews the change, flags what would be overdue, and confirms with the version", async () => {
  const h = harness({
    post: (path, body) => {
      if (body.dry_run) return { dry_run: true, state_version: { log_lines: 40, config_generation: 1 }, preview: { leaving: [], materialize: [], profile_add: [{ id: "day:2026-10-07:x", title: "Page the on-call", overdue: true }], profile_withdraw: [{ id: "day:2026-10-07:y", title: "Post the plan", overdue: false }], profile_reinstate: [], not_materialized: [], blocking_cycles: [] } };
      return { changed: true, event_ids: ["A"], batch_id: "B", version: {} };
    },
  });
  await h.start();
  h.dispatch("profile.open");
  h.dispatch("profile.select", "on-call");
  assert.equal(h.page().button("Confirm the change").props.disabled, true);
  await h.dispatch("profile.preview");
  const p = h.page();
  assert.ok(p.has("Page the on-call") && p.has("would be overdue at once"));
  assert.ok(p.has("Post the plan"));
  await h.dispatch("profile.confirm");
  assert.deepEqual([h.lastPost().body.profile, h.lastPost().body.expected_version], ["on-call", { log_lines: 40, config_generation: 1 }]);
  assert.equal(h.model.ui.modal, null);
});
