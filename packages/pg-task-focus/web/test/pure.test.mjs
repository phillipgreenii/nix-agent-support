import assert from "node:assert/strict";
import { test } from "node:test";
import * as actions from "../assets/actions.mjs";
import { batchOf, editableFields, effectiveMs, isCorrectable, isRetractable, lookupOf, sentence } from "../assets/events.mjs";
import { durationWords, isBlank, plural, relativeDue, safeHref, shortTitle, timerText } from "../assets/format.mjs";
import { cycleTypes, overtimeNow, parseRoute, remainingNow, routeHash, tabTitle } from "../assets/select.mjs";
import { cycleSegments, previewChange, runningMs, withBreak, withEndAt } from "../assets/timeline.mjs";
import { ULID_PATTERN, ulid } from "../assets/ulid.mjs";
import * as zone from "../assets/zone.mjs";
import { cycle, dimmed, model, state } from "./fixtures.mjs";

const NY = "America/New_York";

test("timer text: remaining time, and overtime as a sign", () => {
  assert.equal(timerText(750), "12:30");
  assert.equal(timerText(3725), "1:02:05");
  assert.equal(timerText(0), "00:00");
  assert.equal(timerText(-250), "+04:10");
  assert.equal(timerText(-250.4), "+04:10");
  assert.equal(timerText(-0.5), "+00:00", "any time past the end reads as overtime");
});

test("durations and relative due times never show a negative time", () => {
  assert.equal(durationWords(4500), "1h15m");
  assert.equal(durationWords(300), "5m");
  assert.equal(durationWords(40), "40s");
  assert.equal(durationWords(3600), "1h");
  assert.deepEqual(relativeDue(1000 * 60 * 22, 0), { text: "in 22m", overdue: false });
  assert.deepEqual(relativeDue(0, 1000 * 60 * 12), { text: "overdue 12m", overdue: true });
  assert.deepEqual(relativeDue(0, 1000 * 20), { text: "overdue under a minute", overdue: true });
  assert.equal(relativeDue(1000 * 20, 0).text, "in under a minute");
});

test("small text helpers", () => {
  assert.equal(shortTitle("Deep work cycle"), "Deep");
  assert.equal(shortTitle(""), "");
  assert.equal(plural(1, "task"), "1 task");
  assert.equal(plural(2, "task"), "2 tasks");
  assert.equal(isBlank("  "), true);
  assert.equal(isBlank(""), true);
  assert.equal(isBlank("x"), false);
  assert.equal(safeHref("https://example.test/a"), "https://example.test/a");
  assert.equal(safeHref("javascript:alert(1)"), null, "a link is never a script");
  assert.equal(safeHref("data:text/html,x"), null);
  assert.equal(safeHref("not a url"), null);
});

test("a ULID has the daemon's shape, and sorts by time", () => {
  const fixed = (n) => new Uint8Array(n).fill(255);
  const a = ulid(Date.parse("2026-10-07T13:00:00Z"), fixed);
  const b = ulid(Date.parse("2026-10-07T13:00:01Z"), fixed);
  assert.match(a, ULID_PATTERN);
  assert.match(b, ULID_PATTERN);
  assert.ok(a < b);
  assert.match(ulid(), ULID_PATTERN);
  assert.notEqual(ulid(), ulid());
  assert.equal(a.length, 26);
  assert.match(ulid(2 ** 48 - 1, fixed), ULID_PATTERN, "the largest 48-bit time still starts with a digit up to 7");
});

// ---- zones: the design's rules for the civil times a clock skips and repeats ----

test("a civil time that does not exist is the first valid instant after the gap", () => {
  const at = (v) => zone.inputValueInZone(zone.instantFromInput(v, NY), NY);
  assert.equal(at("2026-03-08T02:30"), "2026-03-08T03:00");
  assert.equal(at("2026-03-08T03:00"), "2026-03-08T03:00");
  assert.equal(at("2026-03-08T01:59"), "2026-03-08T01:59");
  assert.equal(new Date(zone.instantFromInput("2026-03-08T02:30", NY)).toISOString(), "2026-03-08T07:00:00.000Z");
});

test("a civil time that occurs twice is its earlier occurrence", () => {
  assert.equal(new Date(zone.instantFromInput("2026-11-01T01:30", NY)).toISOString(), "2026-11-01T05:30:00.000Z");
  assert.equal(new Date(zone.instantFromInput("2026-11-01T02:00", NY)).toISOString(), "2026-11-01T07:00:00.000Z");
});

test("a zone with a half-hour gap, and a zone that skips a whole day", () => {
  const lh = "Australia/Lord_Howe";
  assert.equal(zone.inputValueInZone(zone.instantFromInput("2026-10-04T02:15", lh), lh), "2026-10-04T02:30");
  const ap = "Pacific/Apia";
  assert.equal(zone.dateInZone(zone.instantFromInput("2011-12-30T12:00", ap), ap), "2011-12-31", "a skipped civil date resolves after the skipped day");
});

test("zones: ordinary conversions, seconds, rejection of what is not a date", () => {
  const ms = zone.instantFromInput("2026-10-07T09:30:15", NY);
  assert.equal(zone.toRFC3339(ms), "2026-10-07T13:30:15.000Z");
  assert.equal(zone.clockInZone(ms, NY), "09:30");
  assert.equal(zone.inputValueSecondsInZone(ms, NY), "2026-10-07T09:30:15");
  assert.equal(zone.dateTimeInZone(ms, "UTC"), "2026-10-07 13:30");
  assert.equal(zone.instantFromInput("2026-02-31T10:00", NY), null);
  assert.equal(zone.instantFromInput("tomorrow", NY), null);
  assert.equal(zone.instantFromInput("2026-10-07T09:30", "Not/AZone"), null);
  assert.equal(zone.isZone("EST5EDT"), true);
  assert.equal(zone.isZone(""), false);
  assert.equal(zone.isZone("Not/AZone"), false);
});

test("civil date arithmetic", () => {
  assert.equal(zone.addDays("2026-10-31", 1), "2026-11-01");
  assert.equal(zone.addDays("2026-03-01", -1), "2026-02-28");
  assert.equal(zone.daysBetween("2026-10-05", "2026-10-11"), 6);
  assert.equal(zone.daysBetween("2026-10-11", "2026-10-05"), -6);
  assert.equal(zone.addDays("nope", 1), null);
});

// ---- routes ----

test("deep links parse and round-trip", () => {
  assert.deepEqual(parseRoute("#/tasks/day:2026-10-07:post-plan"), { name: "task", id: "day:2026-10-07:post-plan" });
  assert.deepEqual(parseRoute("#/tasks/day%3A2026-10-07%3Apost-plan"), { name: "task", id: "day:2026-10-07:post-plan" });
  assert.deepEqual(parseRoute("#/cycles/01JC0000000000000000000001"), { name: "cycle", id: "01JC0000000000000000000001" });
  assert.deepEqual(parseRoute("#/events"), { name: "events" });
  assert.deepEqual(parseRoute("#/events/01JABC"), { name: "events", id: "01JABC" });
  assert.deepEqual(parseRoute(""), { name: "today" });
  assert.deepEqual(parseRoute("#/nonsense/1"), { name: "today" });
  assert.deepEqual(parseRoute("#/tasks/%E0%A4%A"), { name: "task", id: "%E0%A4%A" }, "a bad escape is left as typed");
  for (const r of [{ name: "task", id: "day:2026-10-07:post-plan" }, { name: "cycle", id: "x y" }, { name: "events" }, { name: "events", id: "Z" }, { name: "today" }]) {
    assert.deepEqual(parseRoute(routeHash(r)), r);
  }
});

// ---- selectors ----

test("the cycle types list the profile's first, then the others", () => {
  const types = cycleTypes(model());
  assert.deepEqual(types.map((t) => [t.id, t.inProfile]), [["notifications", true], ["review", true], ["deep-work", true], ["page-response", false]]);
  assert.deepEqual(cycleTypes(model({ config: null })), []);
});

test("the tab title shows the timer or the overtime", () => {
  assert.equal(tabTitle(model()), "pg-task-focus");
  const running = model({ state: state({ focus: cycle({ remaining_seconds: 750 }) }) });
  assert.equal(tabTitle(running), "Deep 12:30");
  const ticking = { ...running, now: running.receivedAt + 5000 };
  assert.equal(tabTitle(ticking), "Deep 12:25", "the timer counts from the daemon's read on the page's own clock");
  const over = model({ state: state({ focus: cycle({ remaining_seconds: -250, overtime: true }) }) });
  assert.equal(tabTitle(over), "Deep +04:10");
  const ro = model({ state: state({ store: { state: "read_only", reason: "r" }, focus: cycle({ remaining_seconds: 60 }) }) });
  assert.equal(tabTitle(ro), "READ-ONLY Deep 01:00");
});

test("a paused cycle's timer does not move, and a running one moves with the page's clock", () => {
  const m = model({ now: T(100000), receivedAt: T(0) });
  assert.equal(remainingNow(m, dimmed()), 780);
  assert.equal(remainingNow(m, cycle({ remaining_seconds: 750 })), 650);
  assert.equal(remainingNow({ ...m, now: T(0) - 5000 }, cycle({ remaining_seconds: 750 })), 750, "a clock that went backwards never adds time");
  assert.equal(overtimeNow(m, cycle({ remaining_seconds: 50 })), true);
  assert.equal(overtimeNow(m, cycle({ remaining_seconds: 500 })), false);
  assert.equal(overtimeNow(m, dimmed({ remaining_seconds: -5 })), false, "only the running cycle is in overtime");
});
function T(ms) {
  return Date.parse("2026-10-07T13:00:00.000Z") + ms;
}

// ---- actions ----

test("every cycle action names its cycle", () => {
  const c = cycle();
  for (const a of [actions.pauseCycle(c), actions.resumeCycle(c), actions.stopCycle(c), actions.boostCycle(c, 5), actions.annotateCycle(c, "n", [])]) {
    assert.equal(a.body.cycle_id, c.id, a.path);
  }
  assert.deepEqual(actions.switchTo(c).body, { to: c.id });
});

test("a completion sends no time unless one was chosen", () => {
  const t = { id: "day:2026-10-07:post-plan", title: "Post the plan" };
  assert.deepEqual(actions.completeTask(t).body, {});
  assert.equal(actions.completeTask(t).path, "/api/v1/tasks/day%3A2026-10-07%3Apost-plan/complete");
  assert.deepEqual(actions.completeTask(t, Date.parse("2026-10-07T13:30:00Z")).body, { effective_at: "2026-10-07T13:30:00.000Z" });
  assert.deepEqual(actions.skipTask(t, "  not needed ").body, { reason: "not needed" });
  for (const a of [actions.completeTask(t), actions.skipTask(t, "x"), actions.stopCycle(cycle())]) assert.ok(a.undo?.label, `${a.path} offers an Undo`);
  for (const a of [actions.pauseCycle(cycle()), actions.startCycle("review"), actions.boostCycle(cycle(), 5)]) assert.equal(a.undo, undefined, `${a.path} offers none`);
});

test("a note is the whole form, without empty pairs", () => {
  const a = actions.annotateCycle(cycle(), "  ", [{ key: "pr", value: "12" }, { key: "ticket", value: "" }, { key: "", value: "x" }, { key: " ticket ", value: "A-1" }]);
  assert.deepEqual(a.body, { cycle_id: cycle().id, kv: [{ key: "pr", value: "12" }, { key: "ticket", value: "A-1" }] });
  assert.equal(actions.annotateCycle(cycle(), "hello", []).body.note, "hello");
});

test("retractions go to the batch when an event is a member of one", () => {
  const item = { id: "01JE" };
  assert.equal(actions.retract(item, "01JB", "", "x").path, "/api/v1/batches/01JB/retract");
  assert.equal(actions.retract(item, "", "", "x").path, "/api/v1/events/01JE/retract");
  assert.deepEqual(actions.retract(item, "", " why ", "x").body, { reason: "why" });
});

// ---- events and the timeline ----

const ev = (id, type, at, data, extra = {}) => ({ id, event: { v: 1, id, at, effective_at: at, type, data }, corrected_by: [], retracted: false, ...extra });

test("an event is one sentence, and says the task's title and not its id", () => {
  const items = [ev("01JM", "task.materialized", "2026-10-07T12:00:00.000Z", { task_id: "day:2026-10-07:post-plan", title: "Post the plan", due: "2026-10-07T13:30:00.000Z", due_rule: { at: "09:30", tz: NY } })];
  const look = lookupOf(items, null);
  assert.match(sentence(items[0], look, NY), /^Task added: Post the plan, due 2026-10-07 09:30 America\/New_York$/);
  const done = ev("01JD", "task.completed", "2026-10-07T13:40:00.000Z", { task_id: "day:2026-10-07:post-plan" });
  assert.equal(sentence(done, look, NY), "Completed: Post the plan");
  const skip = ev("01JS", "task.skipped", "2026-10-07T13:40:00.000Z", { task_id: "x:y", reason: "not today" });
  assert.equal(sentence(skip, look, NY), "Skipped: x:y (reason: not today)");
});

test("which events can be corrected and retracted, and the fields of each", () => {
  for (const t of ["event.corrected", "event.retracted", "batch.committed"]) assert.equal(isCorrectable(t), false, t);
  assert.equal(isCorrectable("cycle.started"), true);
  assert.equal(isRetractable("batch.committed"), false);
  assert.equal(isRetractable("event.retracted"), true, "a retraction can itself be retracted");
  assert.deepEqual(editableFields("cycle.started").map((f) => f.name), ["type", "planned_minutes"]);
  assert.equal(editableFields("cycle.started").find((f) => f.name === "type").kind, "cycle-type", "a cycle's type is correctable");
  assert.deepEqual(editableFields("period.changed", { kind: "day" }).map((f) => f.name), ["tz", "label"]);
  assert.ok(editableFields("period.changed", { kind: "week", end: "x" }).some((f) => f.name === "end"));
  for (const t of ["cycle.started", "task.skipped", "period.changed", "cycle.annotated"]) {
    for (const f of editableFields(t, { end: "x" })) {
      assert.ok(!["cycle_id", "task_id", "target", "batch", "interrupts", "definition", "kind", "start", "cadence", "period", "profile"].includes(f.name), `${t}.${f.name} is an identity field`);
    }
  }
  assert.equal(batchOf({ event: { type: "task.skipped", data: { batch: "B" } } }), "B");
  assert.equal(batchOf({ event: { type: "event.retracted", data: { target_batch: "B" } } }), "");
  assert.equal(effectiveMs(ev("1", "task.completed", "2026-10-07T13:00:00.000Z", {})), Date.parse("2026-10-07T13:00:00.000Z"));
});

const at = (h, m) => Date.parse(`2026-10-07T${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:00.000Z`);
const iso = (h, m) => new Date(at(h, m)).toISOString();

test("a cycle's segments come from its events, and another cycle's start interrupts it", () => {
  const items = [
    ev("1", "cycle.started", iso(9, 0), { cycle_id: "A", type: "deep-work", title: "Deep work cycle", planned_minutes: 50 }),
    ev("2", "cycle.started", iso(9, 30), { cycle_id: "B", type: "review", title: "Review cycle", planned_minutes: 25, interrupts: "A" }),
    ev("3", "cycle.stopped", iso(9, 50), { cycle_id: "B" }),
    ev("4", "cycle.resumed", iso(10, 0), { cycle_id: "A" }),
    ev("5", "cycle.paused", iso(10, 20), { cycle_id: "A" }, { retracted: true }),
    ev("6", "cycle.stopped", iso(10, 40), { cycle_id: "A" }),
  ];
  const rec = cycleSegments(items, "A");
  assert.equal(rec.found, true);
  assert.equal(rec.title, "Deep work cycle");
  assert.deepEqual(rec.segments, [{ start: at(9, 0), end: at(9, 30) }, { start: at(10, 0), end: at(10, 40) }], "a retracted pause is ignored");
  assert.equal(rec.stoppedAt, at(10, 40));
  assert.equal(runningMs(rec.segments, at(12, 0)), (30 + 40) * 60000);
  assert.equal(cycleSegments(items, "nope").found, false);
});

test("insert break carves the interval out, and end at clips the cycle", () => {
  const seg = [{ start: at(9, 0), end: at(13, 30) }];
  assert.deepEqual(withBreak(seg, at(12, 0), at(13, 0)), [{ start: at(9, 0), end: at(12, 0) }, { start: at(13, 0), end: at(13, 30) }]);
  assert.deepEqual(withBreak(seg, at(8, 0), at(10, 0)), [{ start: at(10, 0), end: at(13, 30) }]);
  assert.deepEqual(withBreak(seg, at(14, 0), at(15, 0)), seg, "a break outside the cycle changes nothing");
  assert.deepEqual(withBreak([{ start: at(9, 0), end: null }], at(12, 0), at(13, 0)), [{ start: at(9, 0), end: at(12, 0) }, { start: at(13, 0), end: null }], "an open segment stays open");
  assert.deepEqual(withEndAt(seg, at(11, 0)), [{ start: at(9, 0), end: at(11, 0) }]);
  assert.deepEqual(withEndAt([{ start: at(9, 0), end: null }], at(11, 0)), [{ start: at(9, 0), end: at(11, 0) }]);
  assert.deepEqual(withEndAt(seg, at(8, 0)), []);
  const rec = { segments: seg };
  const p = previewChange(rec, { kind: "break", from: at(12, 0), to: at(13, 0) }, at(14, 0));
  assert.equal(p.before.ms, 270 * 60000);
  assert.equal(p.after.ms, 210 * 60000);
  assert.equal(p.unchanged, false);
  assert.equal(previewChange(rec, { kind: "endat", at: at(15, 0) }, at(16, 0)).unchanged, true, "ending after the stop changes nothing");
});
