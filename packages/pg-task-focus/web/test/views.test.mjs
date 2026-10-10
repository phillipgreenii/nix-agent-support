import assert from "node:assert/strict";
import { test } from "node:test";
import { viewApp } from "../assets/view-app.mjs";
import { button, control } from "../assets/view-common.mjs";
import { cyclePanel } from "../assets/view-cycles.mjs";
import { render } from "../assets/vdom.mjs";
import { cycle, config, dimmed, model, period, readOnlyStore, state, task, T0, iso } from "./fixtures.mjs";
import { newRoot } from "./fakedom.mjs";
import { page } from "./page.mjs";

/** A dispatch that records the intents a view raises. */
function spy() {
  const calls = [];
  const dispatch = (name, ...args) => {
    calls.push([name, ...args]);
  };
  return { calls, dispatch };
}

const draw = (m, s = spy()) => ({ p: page(viewApp(m, s.dispatch)), ...s });

// ---- the controls ----

test("a control that does not say whether it mutates cannot be built", () => {
  const m = model();
  assert.throws(() => button(m, {}, "x"), /must say whether it mutates/);
  assert.throws(() => control(m, "input", { type: "text" }), /must say whether it mutates/);
  assert.equal(button(m, { mutates: false }, "x").props.disabled, false);
  const ro = model({ state: state({ store: readOnlyStore }) });
  assert.equal(button(ro, { mutates: true }, "x").props.disabled, true, "read-only disables a mutating control");
  assert.equal(button(ro, { mutates: false }, "x").props.disabled, false, "and leaves the others");
  assert.equal(button(m, { mutates: true, disabled: true }, "x").props.disabled, true);
});

// ---- read-only mode ----

function everythingOpen(over = {}) {
  // A page with every kind of mutating control on it.
  const f = cycle();
  const d = dimmed();
  return model({
    undo: { seq: 1, label: "Completed Plan the day", ref: { event: "01JE" }, expiresAt: T0 + 9000 },
    ui: {
      taskForm: { taskId: "day:2026-10-07:post-plan", mode: "skip", at: "", reason: "r", problem: "" },
      reasons: ["not needed"],
    },
    state: state({
      focus: f,
      dimmed: [d],
      resume_offer: { cycle: d, action: "switch" },
      periods: [period("day", { ended: true, banner: "New day: roll over", today: "2026-10-08" }), period("week"), period("sprint")],
      next: task(),
    }),
    ...over,
  });
}

test("read-only mode: the banner is first, says the daemon's sentence, and every mutating control is disabled", () => {
  const m = everythingOpen();
  m.state.store = readOnlyStore;
  const { p } = draw(m);
  const banner = p.byAttr("data-banner", "read-only")[0];
  assert.ok(banner, "there is a READ-ONLY banner");
  assert.equal(banner.props.role, "alert", "it is announced when it appears");
  assert.equal(p.text().includes("READ-ONLY: the append fsync failed. Restart pg-task-focus to recover"), true);
  assert.equal(page(banner).text(), "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover");
  // It is the first thing in the banners, which stick to the top of every area.
  const banners = p.where((n) => n.props.id === "banners")[0];
  assert.equal(banners.children[0], banner);

  const mutators = p.mutators();
  assert.ok(mutators.length >= 14, `a populated page has many mutating controls, found ${mutators.length}`);
  for (const n of mutators) assert.equal(n.props.disabled, true, `${n.tag} ${JSON.stringify(n.props["aria-label"] ?? n.props.id ?? "")} is disabled`);
});

test("read-only mode in the other areas: the editor's actions and the modals' confirm are disabled too", () => {
  const items = [
    { id: "01JE1", event: { v: 1, id: "01JE1", at: iso(T0), effective_at: iso(T0), type: "task.completed", data: { task_id: "x" } }, corrected_by: [], retracted: false },
    { id: "01JE2", event: { v: 1, id: "01JE2", at: iso(T0), effective_at: iso(T0), type: "task.skipped", data: { task_id: "x", reason: "r", batch: "01JB" } }, corrected_by: ["01JE3"], retracted: false, original: { v: 1, id: "01JE2", at: iso(T0), effective_at: iso(T0), type: "task.skipped", data: { task_id: "x", reason: "old", batch: "01JB" } } },
  ];
  const m = everythingOpen({
    route: { name: "events" },
    ui: {
      editor: { view: "corrected", from: "2026-10-05", to: "", type: "", items, loading: false, error: null, showOriginal: {}, correct: null, rowError: null, op: { kind: "endat", cycleId: "", from: "", to: "", at: "", problem: "", error: null }, cycleItems: [], missing: "" },
    },
  });
  m.state.store = readOnlyStore;
  const { p } = draw(m);
  assert.equal(p.byAttr("data-banner", "read-only").length, 1, "the banner is in every area");
  const names = p.mutators().map((n) => n.props["aria-label"] ?? n.children[0]?.text ?? n.props.id);
  assert.ok(names.some((n) => /Correct/.test(n)), `Correct is on the page: ${names}`);
  assert.ok(names.some((n) => /Retract/.test(n)));
  assert.ok(names.includes("Insert break (from, to)") && names.includes("End at (time)"));
  for (const n of p.mutators()) assert.equal(n.props.disabled, true, String(n.props["aria-label"] ?? n.props.id));
  // Reading still works: the original view is one action away.
  assert.equal(p.button(/Show the original of task.skipped/).props.disabled, false);

  const withModal = everythingOpen({ ui: { modal: "profile", profile: { selected: "on-call", preview: { profile: "on-call", result: { dry_run: true, state_version: { log_lines: 1, config_generation: 1 }, preview: { leaving: [], materialize: [], profile_add: [], profile_withdraw: [], profile_reinstate: [], not_materialized: [], blocking_cycles: [] } } }, previewing: false, error: null } } });
  withModal.state.store = readOnlyStore;
  const q = draw(withModal).p;
  assert.equal(q.button("Confirm the change").props.disabled, true);
  for (const n of q.mutators()) assert.equal(n.props.disabled, true);
});

test("a healthy store shows no banner and leaves the controls enabled", () => {
  const { p } = draw(everythingOpen());
  assert.equal(p.byAttr("data-banner", "read-only").length, 0);
  assert.ok(p.mutators().some((n) => !n.props.disabled));
  assert.equal(p.button("Pause Deep work cycle").props.disabled, false);
});

// ---- the cycle panel ----

test("the running cycle is the focus, with its timer and its controls", () => {
  const m = model({ state: state({ focus: cycle({ remaining_seconds: 750 }) }) });
  const { p, calls } = draw(m);
  const timer = p.where((n) => n.props.role === "timer")[0];
  assert.equal(page(timer).text(), "12:30");
  for (const name of ["Pause Deep work cycle", "Stop Deep work cycle"]) assert.ok(p.hasButton(name), name);
  for (const mins of [5, 10, 25]) assert.ok(p.hasButton(`Boost Deep work cycle by ${mins} minutes`), `boost ${mins}`);
  assert.ok(p.has("Planned 50m"));
  p.click("Pause Deep work cycle");
  p.click("Boost Deep work cycle by 10 minutes");
  assert.deepEqual(calls.map((c) => c[0]), ["cycle.pause", "cycle.boost"]);
  assert.equal(calls[0][1].id, cycle().id, "the pause names the cycle");
  assert.equal(calls[1][2], 10);
});

test("overtime is a negative time with words and a glyph, not a colour", () => {
  const m = model({ state: state({ focus: cycle({ remaining_seconds: -250, overtime: true }) }) });
  const { p } = draw(m);
  assert.equal(page(p.where((n) => n.props.role === "timer")[0]).text(), "+04:10");
  assert.ok(p.has("Overtime"));
  assert.ok(p.where((n) => n.props.class?.includes("overtime")).length > 0);
  assert.equal(p.where((n) => n.props.role === "timer")[0].props["aria-label"], "Deep work cycle: overtime");
});

test("the timer ticks on the page's clock between reads", () => {
  const m = model({ state: state({ focus: cycle({ remaining_seconds: 5 }) }) });
  assert.equal(page(draw(m).p.where((n) => n.props.role === "timer")[0]).text(), "00:05");
  m.now += 8000;
  assert.equal(page(draw(m).p.where((n) => n.props.role === "timer")[0]).text(), "+00:03");
});

test("paused cycles stay visible beneath the focus, dimmed, in words, with Switch and Stop", () => {
  const m = model({ state: state({ focus: cycle(), dimmed: [dimmed()] }) });
  const { p, calls } = draw(m);
  const li = p.where((n) => n.tag === "li" && n.props["data-cycle-id"] === dimmed().id)[0];
  assert.ok(li.props.class.includes("dimmed-cycle"));
  const inner = page(li);
  assert.ok(inner.has("Paused"), "paused is said in words");
  assert.ok(inner.has("Ran 12m of 25m"));
  assert.ok(p.hasButton("Switch to Review cycle"));
  assert.ok(p.hasButton("Stop Review cycle"), "a dimmed cycle can be stopped without switching to it");
  assert.equal(p.hasButton("Resume Review cycle"), false, "while one runs the action is Switch");
  p.click("Switch to Review cycle");
  p.click("Stop Review cycle");
  assert.deepEqual(calls.map((c) => c[0]), ["cycle.switch", "cycle.stop"]);
  assert.equal(calls[0][1].id, dimmed().id);
  // Boost and a note are reachable while dimmed.
  assert.ok(p.hasButton("Boost Review cycle by 5 minutes"));
  assert.ok(p.hasButton("Save the note of Review cycle"));
});

test("with nothing running, a paused cycle offers Resume, never Switch", () => {
  const d = dimmed({ can_switch: false });
  const m = model({ state: state({ focus: null, dimmed: [d, dimmed({ id: "01JC0000000000000000000003", title: "Page response", can_switch: false })] }) });
  const { p } = draw(m);
  assert.ok(p.hasButton("Resume Review cycle"));
  assert.ok(p.hasButton("Resume Page response"));
  assert.equal(p.hasButton(/^Switch/), false);
  assert.ok(p.has("No cycle is running."));
});

test("the resume offer is persistent, and is Switch while another cycle runs", () => {
  const d = dimmed();
  const m = model({ state: state({ focus: cycle(), dimmed: [d], resume_offer: { cycle: d, action: "switch" } }) });
  const { p, calls } = draw(m);
  assert.ok(p.has("Resume Review cycle?"));
  assert.ok(p.hasButton("Switch to Review cycle (offered)"));
  p.click("Switch to Review cycle (offered)");
  p.click("Switch to Review cycle");
  assert.deepEqual(calls.map((c) => c[0]), ["cycle.switch", "cycle.switch"], "the offer and the dimmed row do the same thing");
});

test("the resume offer names its action", () => {
  const d = dimmed({ can_switch: false });
  const m = model({ state: state({ focus: null, dimmed: [d], resume_offer: { cycle: d, action: "resume" } }) });
  const { p, calls } = draw(m);
  const offer = p.byAttr("data-offer", d.id)[0];
  assert.ok(page(offer).has("Resume Review cycle?"));
  page(offer).click("Resume Review cycle (offered)");
  assert.equal(calls[0][0], "cycle.resume");
});

test("a cycle outside the active profile is flagged, and starting one while another runs says it will pause it", () => {
  const m = model({ state: state({ focus: cycle({ not_in_profile: true }) }) });
  const { p } = draw(m);
  assert.ok(p.has("not in the active profile"));
  assert.ok(p.byAttr("data-hint", "interrupt")[0]);
  assert.ok(page(p.byAttr("data-hint", "interrupt")[0]).has("Starting a cycle pauses Deep work cycle"));
  const opts = p.where((n) => n.tag === "option").map((n) => page(n).text());
  assert.deepEqual(opts, ["Notification cycle", "Review cycle", "Deep work cycle", "Page response (not in the active profile)"]);
});

test("the start form sends the chosen type and the minutes override", () => {
  const { p, calls } = draw(model());
  p.type("Type", "review");
  p.type("Minutes", "40");
  p.submitOf("Start a cycle");
  assert.deepEqual(calls.map((c) => c[0]), ["cycleStart.field", "cycleStart.field", "cycleStart.submit"]);
  assert.deepEqual(calls[0].slice(1), ["type", "review"]);
  assert.deepEqual(calls[1].slice(1), ["minutes", "40"]);
});

test("the note form is pre-filled from the type's keys and is optional", () => {
  const m = model({ state: state({ focus: cycle({ type: "deep-work", kv: [{ key: "ticket", value: "ABC-1" }], note: "wrote it" }) }) });
  const { p, calls } = draw(m);
  const keys = p.where((n) => n.tag === "input" && n.props.pattern === "[a-z0-9_-]+").map((n) => n.props.value);
  assert.deepEqual(keys, ["ticket", "pr"], "the pairs of the cycle, then a blank row for each key of its type it has no pair for");
  assert.equal(p.control("Note").props.value, "wrote it");
  // Stopping asks for nothing: Stop dispatches at once.
  p.click("Stop Deep work cycle");
  assert.deepEqual(calls.map((c) => c[0]), ["cycle.stop"]);
});

test("the compact panel of the editor area leaves out the start form but keeps the controls", () => {
  const m = model({ state: state({ focus: cycle() }) });
  const p = page(cyclePanel(m, () => {}, { compact: true }));
  assert.ok(p.hasButton("Pause Deep work cycle"));
  assert.equal(p.has("Start a cycle"), false);
});

// ---- the header and the checklists ----

test("the header shows the periods, the profile, what is next, and the ended banner exactly", () => {
  const m = everythingOpen();
  const { p, calls } = draw(m);
  assert.ok(p.has("Day: 2026-10-07"));
  assert.ok(p.has("Week: 2026-10-05 to 2026-10-11"));
  assert.ok(p.has("America/New_York"));
  assert.ok(p.has("Profile: normal"));
  assert.ok(p.hasButton("New day: roll over"), "the exact banner string is the next action");
  assert.ok(p.has("Next: Post the plan for the day, due 09:30 America/New_York, in 30m"));
  assert.equal(p.buttons("New day: roll over").length, 1, "one control, in the header");
  p.click("New day: roll over");
  assert.equal(calls[0][0], "period.open");
  // The ended day says so where its tasks are.
  assert.ok(p.has("This day has ended. New day: roll over."));
});

test("an overdue next task is said in words and with a glyph", () => {
  const m = model({ state: state({ next: task({ due: "2026-10-07T12:48:00.000Z", overdue: true }) }) });
  const { p } = draw(m);
  assert.ok(p.has("overdue 12m"));
  assert.ok(p.byAttr("data-next", task().id)[0].props.class.includes("next-overdue"));
});

test("with an empty log the page invites the operator to set up, and shows an empty state", () => {
  const m = model({ state: state({ initialized: false, profile: undefined, periods: [], tasks: [] }) });
  const { p, calls } = draw(m);
  assert.ok(p.has("Set your day, week and sprint to begin."));
  assert.ok(p.has("Nothing is set up yet."));
  p.click("Set up");
  p.click("Set up the day, week and sprint");
  assert.deepEqual(calls.map((c) => c[0]), ["period.open", "period.open"]);
});

test("each period has an empty state, a task shows its status in words and its due time with a zone", () => {
  const m = model({ state: state({ tasks: [task({ id: "day:2026-10-07:a", title: "A", status: "completed", resolved_at: "2026-10-07T13:12:00.000Z", due: "2026-10-07T13:00:00.000Z" }), task({ id: "day:2026-10-07:b", title: "B", status: "skipped", reason: "not today" }), task({ id: "day:2026-10-07:c", title: "C", status: "missed" }), task({ id: "day:2026-10-07:d", title: "D", status: "withdrawn" }), task({ id: "day:2026-10-07:e", title: "E", due: "2026-10-07T12:00:00.000Z", overdue: true })] }) });
  const { p } = draw(m);
  const row = (id) => page(p.byAttr("data-task-id", id)[0]);
  assert.ok(row("day:2026-10-07:a").has("Done 09:12 America/New_York"));
  assert.ok(row("day:2026-10-07:a").has("due 09:00 America/New_York"));
  assert.ok(row("day:2026-10-07:b").has("Skipped"));
  assert.ok(row("day:2026-10-07:b").has("Reason: not today"));
  assert.ok(row("day:2026-10-07:c").has("Missed"));
  assert.ok(row("day:2026-10-07:d").has("Not in the active profile"));
  assert.ok(row("day:2026-10-07:e").has("overdue 1h"));
  assert.ok(p.has("No tasks in this week for the active profile."), "the week and sprint have none of these tasks, and say so");
  // Done, skipped and withdrawn tasks offer nothing; an open or missed one offers all three.
  assert.equal(row("day:2026-10-07:a").buttons("Done: A").length, 0);
  assert.equal(row("day:2026-10-07:d").buttons(/Done/).length, 0);
  assert.equal(row("day:2026-10-07:c").buttons(/Done|Skip/).length, 3, "a missed task can still be completed or skipped");
  assert.equal(row("day:2026-10-07:e").buttons(/Done|Skip/).length, 3);
});

test("Done is one action that raises one intent, with no time", () => {
  const { p, calls } = draw(model());
  p.click("Done: Post the plan for the day");
  assert.equal(calls.length, 1);
  assert.equal(calls[0][0], "task.done");
  assert.equal(calls[0][1].id, "day:2026-10-07:post-plan");
});

test("skipping asks for a reason inline, offers the recent ones, and cannot be confirmed blank", () => {
  const m = model({ ui: { reasons: ["not needed today"], taskForm: { taskId: "day:2026-10-07:post-plan", mode: "skip", at: "", reason: "", problem: "" } } });
  const { p, calls } = draw(m);
  assert.equal(p.button("Skip task").props.disabled, true, "a blank reason cannot be confirmed");
  assert.deepEqual(p.where((n) => n.tag === "option" && n.props.value === "not needed today").length, 1, "the recent reason is offered");
  p.type("Reason for skipping", "busy");
  assert.deepEqual(calls[0], ["task.field", "reason", "busy"]);
  const m2 = model({ ui: { taskForm: { taskId: "day:2026-10-07:post-plan", mode: "skip", at: "", reason: "busy", problem: "" } } });
  assert.equal(draw(m2).p.button("Skip task").props.disabled, false);
});

test("Done at offers a time that defaults to now, in a named zone", () => {
  const m = model({ ui: { taskForm: { taskId: "day:2026-10-07:post-plan", mode: "doneAt", at: "2026-10-07T09:00", reason: "", problem: "" } } });
  const { p } = draw(m);
  const input = p.control("Done at (time in America/New_York)");
  assert.equal(input.props.type, "datetime-local");
  assert.equal(input.props.value, "2026-10-07T09:00");
});

// ---- messages ----

test("an error shows the daemon's own sentence, its code and trace id", () => {
  const m = model({
    error: { seq: 1, action: {}, id: "01JX", kind: "problem", status: 409, reason: "task_already_resolved", detail: "Post the plan was already completed by event …ABCDEFGH at 2026-10-07T13:00:00Z (09:00 America/New_York).", traceId: "abc123", details: { events: ["01JABCDEFGH"] }, retryable: false, readOnly: false, candidates: [] },
  });
  const { p } = draw(m);
  const box = p.byAttr("data-reason", "task_already_resolved")[0];
  assert.equal(box.props.role, "alert");
  const t = page(box).text();
  assert.ok(t.includes("Post the plan was already completed by event …ABCDEFGH at 2026-10-07T13:00:00Z (09:00 America/New_York)."), "the sentence is the daemon's, unchanged");
  assert.ok(t.includes("task_already_resolved") && t.includes("trace abc123"));
  assert.ok(box && page(box).where((n) => n.tag === "a" && n.props.href === "#/events/01JABCDEFGH").length === 1, "a named event links into the editor");
  assert.equal(page(box).hasButton("Repeat the request"), false, "a refusal is not repeated as it was");
});

test("an unknown outcome offers a repeat that keeps the same request id", () => {
  const m = model({ error: { seq: 1, action: {}, id: "01JX", kind: "network", status: 0, reason: "", detail: "The service could not be reached, so the outcome of this request is unknown.", traceId: "", details: null, retryable: true, readOnly: false, candidates: [] } });
  const { p, calls } = draw(m);
  assert.ok(p.has("The service could not be reached, so the outcome of this request is unknown."));
  assert.ok(p.has("same request id"));
  p.click("Repeat the request");
  assert.equal(calls[0][0], "error.retry");
});

test("an ambiguous cycle verb is shown as a choice, never decided by the page", () => {
  const m = model({ error: { seq: 1, action: { path: "/api/v1/cycles/pause", body: {} }, id: "01JX", kind: "problem", status: 400, reason: "cycle_ambiguous", detail: "Two cycles could be meant.", traceId: "t", details: { cycles: [{ id: "A", title: "Deep work cycle" }, { id: "B", title: "Review cycle" }] }, retryable: false, readOnly: false, candidates: [{ id: "A", title: "Deep work cycle" }, { id: "B", title: "Review cycle" }] } });
  const { p, calls } = draw(m);
  assert.ok(p.has("Which cycle do you mean?"));
  p.click("Use Review cycle");
  assert.deepEqual(calls[0], ["error.choose", "B"]);
});

test("a no-op is a quiet notice, not an error", () => {
  const m = model({ notice: { seq: 1, text: "Review cycle has been paused since 13:00:00Z (09:00 America/New_York)" } });
  const { p } = draw(m);
  const n = p.byAttr("role", "status").find((x) => page(x).has("has been paused since"));
  assert.ok(n);
  const live = p.byAttr("id", "live")[0];
  assert.equal(p.where((x) => x.props.role === "alert" && !page(live).all().includes(x)).length, 0, "nothing on the page but the empty live region is an alert");
  assert.equal(p.where((x) => x.props.class?.includes("error")).length, 0);
});

test("the undo is a focusable button that is announced and says its limit", () => {
  const m = model({ undo: { seq: 3, label: "Completed Plan the day", ref: { event: "01JE" }, expiresAt: T0 + 10000 } });
  const { p, calls } = draw(m);
  const toast = p.byAttr("id", "undo-toast")[0];
  assert.equal(toast.props.role, "status");
  assert.ok(page(toast).has("Completed Plan the day."));
  assert.ok(page(toast).has("Available for 10 seconds"));
  assert.ok(page(toast).has("Undo is refused once later events depend on it"));
  p.click("Undo");
  assert.equal(calls[0][0], "undo");
  m.now = T0 + 10001;
  assert.equal(draw(m).p.hasButton("Undo"), false, "after ten seconds it is gone");
});

test("a lost connection is said, with the time of the state shown", () => {
  const m = model({ conn: "lost" });
  const { p, calls } = draw(m);
  assert.ok(p.has("The connection to the service was lost."));
  p.click("Try now");
  assert.equal(calls[0][0], "refresh");
});

test("the page before the first read says so, and a failed first read says why", () => {
  const m = model({ loaded: false, state: null });
  assert.ok(draw(m).p.has("Reading the state"));
  const failed = model({ loaded: false, state: null, loadError: { detail: "The service could not be reached, so the outcome of this request is unknown." } });
  assert.ok(draw(failed).p.has("could not be reached"));
});

// ---- the page as a whole ----

test("the whole page renders into a DOM and re-renders without moving what did not change", () => {
  const { root, doc } = newRoot();
  const m = everythingOpen({ now: T0 - 30000 });
  const s = spy();
  render(root, viewApp(m, s.dispatch));
  const moves = doc.moves;
  const first = root.byTag("button").length;
  assert.ok(first > 20);
  m.now += 1000;
  render(root, viewApp(m, s.dispatch));
  assert.equal(doc.moves, moves, "a tick moves no element");
  assert.equal(root.byTag("dialog").length, 2, "both dialogs are in the page, closed");
  assert.ok(root.byTag("dialog").every((d) => d.open === false));
});
