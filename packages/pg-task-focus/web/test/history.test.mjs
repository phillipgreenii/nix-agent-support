import assert from "node:assert/strict";
import { test } from "node:test";
import { cycle, state } from "./fixtures.mjs";
import { harness } from "./harness.mjs";

const NY = "America/New_York";
const wait = (ms = 20) => new Promise((r) => setTimeout(r, ms));
const item = (id, type, iso, data, extra = {}) => ({ id, event: { v: 1, id, at: iso, effective_at: iso, type, data }, corrected_by: [], retracted: false, ...extra });

const LOG = [
  item("01JM", "task.materialized", "2026-10-06T12:00:00.000Z", { task_id: "day:2026-10-06:post-plan", definition: "post-plan", cadence: "daily", period: "2026-10-06", title: "Post the plan", due: "2026-10-06T13:30:00.000Z", due_rule: { at: "09:30", tz: NY }, batch: "B" }),
  item("01JD", "task.completed", "2026-10-06T13:20:00.000Z", { task_id: "day:2026-10-06:post-plan" }),
  item("01JC1", "cycle.started", "2026-10-06T13:00:00.000Z", { cycle_id: "OLD", type: "review", title: "Review cycle", planned_minutes: 25 }),
  item("01JC2", "cycle.stopped", "2026-10-06T13:40:00.000Z", { cycle_id: "OLD" }),
  item("01JC3", "cycle.annotated", "2026-10-06T13:41:00.000Z", { cycle_id: "OLD", note: "reviewed it", kv: [{ key: "pr", value: "9" }] }),
];

const events = (q) => ({ view: "corrected", events: LOG.filter((i) => !q.types || q.types.includes(i.event.type)) });

test("a deep link to a task of an earlier period shows what the log says about it, with links into the editor", async () => {
  const h = harness({ hash: "#/tasks/day:2026-10-06:post-plan", events });
  await h.start();
  await wait();
  const p = h.page();
  const card = p.byAttr("data-detail", "task")[0];
  assert.ok(card);
  assert.ok(p.has("Post the plan"));
  assert.ok(p.has("Its last known state in the log: done."));
  assert.ok(p.has("Completed: Post the plan"));
  assert.ok(p.where((n) => n.tag === "a" && n.props.href === "#/events/01JD").length === 1);
  assert.ok(p.hasButton("Pause Deep work cycle") === false);
});

test("a deep link to a stopped cycle shows its running time, its note and its pairs", async () => {
  const h = harness({ hash: "#/cycles/OLD", events });
  await h.start();
  await wait();
  const p = h.page();
  assert.ok(p.byAttr("data-detail", "cycle")[0]);
  assert.ok(p.has("Review cycle"));
  assert.ok(p.has("Stopped at 2026-10-06 09:40 America/New_York"));
  assert.ok(p.has("Running time 40m"));
  assert.ok(p.has("Note: reviewed it"));
  assert.ok(p.has("pr: 9"));
});

test("an id the log does not know is said in words, and the rest of the page is intact", async () => {
  const h = harness({ hash: "#/tasks/day:1999-01-01:nothing", events });
  await h.start();
  await wait();
  const p = h.page();
  assert.ok(p.has("The log has no task with the id day:1999-01-01:nothing."));
  assert.ok(p.has("Do the capacity check"), "the checklists are still there");
  assert.ok(p.hasButton("Start cycle"));
});

test("a task or cycle in the current state is marked where the page shows it, and the log is not read for it", async () => {
  const h = harness({ hash: `#/cycles/${cycle().id}`, state: state({ focus: cycle() }), events });
  await h.start();
  await wait();
  assert.equal(h.page().byAttr("data-detail", "cycle").length, 0);
  assert.equal(h.model.focusRequest.kind, "cycle");
  assert.equal(h.calls.filter((c) => c.get === "events" && c.q.types?.includes("cycle.started")).length, 0);
  const marked = h.page().where((n) => n.props["data-cycle-id"] === cycle().id)[0];
  assert.equal(marked.props["aria-current"], "true");
});

test("an unreadable log says so", async () => {
  const h = harness({ hash: "#/tasks/x", events: () => ({ view: "corrected", events: LOG }) });
  h.api.getEvents = async () => {
    throw Object.assign(new Error("x"), { detail: "The events could not be read." });
  };
  await h.start();
  await wait();
  assert.ok(h.page().has("The events could not be read."));
});
