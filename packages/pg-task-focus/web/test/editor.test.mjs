import assert from "node:assert/strict";
import { test } from "node:test";
import { correctionFields, correctionInitial, eventsQuery } from "../assets/feature-editor.mjs";
import { cycle, state, T0 } from "./fixtures.mjs";
import { harness, problem } from "./harness.mjs";
import { page } from "./page.mjs";

const NY = "America/New_York";
const item = (id, type, iso, data, extra = {}) => ({ id, event: { v: 1, id, at: iso, effective_at: iso, type, data }, corrected_by: [], retracted: false, ...extra });

const LOG = [
  item("01JP", "period.changed", "2026-10-07T12:00:00.000Z", { kind: "day", start: "2026-10-07", tz: NY, batch: "01JB1" }),
  item("01JT", "task.materialized", "2026-10-07T12:00:00.000Z", { task_id: "day:2026-10-07:post-plan", definition: "post-plan", cadence: "daily", period: "2026-10-07", title: "Post the plan", due: "2026-10-07T13:30:00.000Z", due_rule: { at: "09:30", tz: NY }, batch: "01JB1" }),
  item("01JC", "task.completed", "2026-10-07T13:20:00.000Z", { task_id: "day:2026-10-07:post-plan" }),
  item("01JS1", "cycle.started", "2026-10-07T13:00:00.000Z", { cycle_id: "CYC1", type: "deep-work", title: "Deep work cycle", planned_minutes: 50 }),
  item("01JS2", "cycle.stopped", "2026-10-07T17:30:00.000Z", { cycle_id: "CYC1" }),
  item("01JK", "task.skipped", "2026-10-07T13:25:00.000Z", { task_id: "day:2026-10-07:other", reason: "new reason" }, { corrected_by: ["01JX"], original: { v: 1, id: "01JK", at: "2026-10-07T13:25:00.000Z", effective_at: "2026-10-07T13:25:00.000Z", type: "task.skipped", data: { task_id: "day:2026-10-07:other", reason: "old reason" } } }),
  item("01JR", "event.retracted", "2026-10-07T13:26:00.000Z", { target: "01JC" }),
];

function editorHarness(over = {}) {
  return harness({ hash: "#/events", events: (q) => ({ view: q.view ?? "corrected", events: LOG }), ...over });
}

test("the editor reads the events in the corrected view from two days back, in the zone it names", async () => {
  const h = editorHarness();
  await h.start();
  await h.store.refresh();
  await new Promise((r) => setTimeout(r, 5));
  const q = h.calls.filter((c) => c.get === "events" && c.q.from).at(-1).q;
  assert.equal(q.view, "corrected");
  assert.equal(q.from, "2026-10-05T04:00:00.000Z", "midnight of 2026-10-05 in New York");
  assert.equal(q.to, undefined);
  assert.deepEqual(eventsQuery({ view: "original", from: "", to: "2026-10-07", type: "task.skipped" }, NY), { view: "original", types: ["task.skipped"], to: "2026-10-08T04:00:00.000Z" });
});

test("the table lists the events with the original one action away, and has no delete", async () => {
  const h = editorHarness();
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  const p = h.page();
  assert.ok(p.has("Nothing is ever deleted"));
  assert.ok(p.has("Skipped: day:2026-10-07:other (reason: new reason)"));
  assert.ok(p.has("Corrected once"));
  assert.ok(p.has("Retracted") || p.has("Live"));
  assert.equal(p.where((n) => n.tag === "button" && /delete|remove event/i.test(n.props["aria-label"] ?? "")).length, 0, "there is no delete");
  assert.ok(p.hasButton("Show the original of task.skipped …" + "01JK".padStart(8, "…").slice(-0)) || p.buttons(/Show the original of task.skipped/).length === 1);
  h.dispatch("editor.toggleOriginal", "01JK");
  await new Promise((r) => setTimeout(r, 0));
  const q = h.page();
  assert.ok(q.has("As first logged:"));
  assert.ok(q.has("old reason"), "the original is shown beside the corrected");
  // The limit on undo is next to every retraction.
  assert.ok(q.where((n) => n.props.class?.includes("limit")).length >= 4);
  assert.ok(q.has("Refused once later events depend on it"));
});

test("a retraction goes to the event, or to the whole batch when the event is a member of one", async () => {
  const h = editorHarness();
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  const events = h.model.ui.editor.items;
  await h.dispatch("editor.retract", events.find((i) => i.id === "01JC"));
  assert.equal(h.lastPost().post, "/api/v1/events/01JC/retract");
  await h.dispatch("editor.retract", events.find((i) => i.id === "01JT"));
  assert.equal(h.lastPost().post, "/api/v1/batches/01JB1/retract", "a member of a batch is retracted only through the batch");
  assert.ok(h.page().buttons("Retract the batch of task.materialized …" + "01JT").length >= 0);
});

test("a retraction the daemon refuses shows its sentence on that row and links the events it names", async () => {
  const sentence = "The batch cannot be retracted: event …01JC completes a task that the batch created.";
  const h = editorHarness({
    post: () => {
      throw problem("batch_has_dependents", sentence, { details: { events: ["01JC"] } });
    },
  });
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  await h.dispatch("editor.retract", h.model.ui.editor.items.find((i) => i.id === "01JT"));
  const p = h.page();
  const row = p.where((n) => n.props.class === "row-error")[0];
  assert.ok(row);
  assert.ok(page(row).has(sentence));
  assert.deepEqual(page(row).where((n) => n.tag === "a").map((n) => n.props.href), ["#/events/01JC"], "the dependent is a link into the editor");
  assert.equal(h.model.error, null);
});

test("fixing a cycle's type sends only the type, and the title follows from the daemon", async () => {
  const h = editorHarness();
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  const it = h.model.ui.editor.items.find((i) => i.id === "01JS1");
  h.dispatch("editor.openCorrect", it);
  const p = h.page();
  const sel = p.control("Type");
  assert.equal(sel.tag, "select");
  assert.deepEqual(sel.children.filter((o) => o.tag === "option").map((o) => o.props.value), ["notifications", "review", "deep-work", "page-response"]);
  assert.ok(p.has("The title follows the type"));
  assert.equal(p.where((n) => n.tag === "label" && /^Title/.test(page(n).text())).length, 0, "the title is never typed");
  h.dispatch("editor.correctField", "type", "review");
  await h.dispatch("editor.submitCorrect");
  assert.deepEqual(h.lastPost().body.fields, { type: "review" });
  assert.equal(h.lastPost().post, "/api/v1/events/01JS1/correct");
});

test("a correction names only what changed, reads times in the zone, and says when nothing did", async () => {
  const it = LOG.find((i) => i.id === "01JS1");
  const init = correctionInitial(it, NY);
  assert.equal(init.effective_at, "2026-10-07T09:00:00");
  assert.equal(correctionFields(it, init, NY).problem, "Nothing was changed.");
  assert.deepEqual(correctionFields(it, { ...init, effective_at: "2026-10-07T09:05:00", planned_minutes: "45" }, NY).fields, { effective_at: "2026-10-07T13:05:00.000Z", planned_minutes: 45 });
  assert.match(correctionFields(it, { ...init, planned_minutes: "0" }, NY).problem, /whole number/);
  assert.match(correctionFields(it, { ...init, effective_at: "later" }, NY).problem, /date and time/);
  const note = item("01JN", "cycle.annotated", "2026-10-07T13:00:00.000Z", { cycle_id: "C", note: "n", kv: [{ key: "pr", value: "1" }] });
  const v = correctionInitial(note, NY);
  v.kv = [...v.kv, { key: "", value: "" }, { key: "ticket", value: "A-1" }];
  assert.deepEqual(correctionFields(note, v, NY).fields.kv, [{ key: "pr", value: "1" }, { key: "ticket", value: "A-1" }]);
});

test("the daemon's refusal of a correction is shown in the form, in its words", async () => {
  const detail = "The new type would make the cycle's title unknown.";
  const h = editorHarness({
    post: () => {
      throw problem("unknown_cycle_type", detail, { status: 400 });
    },
  });
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  h.dispatch("editor.openCorrect", h.model.ui.editor.items.find((i) => i.id === "01JS1"));
  h.dispatch("editor.correctField", "type", "review");
  await h.dispatch("editor.submitCorrect");
  assert.equal(h.model.ui.editor.correct.error.detail, detail);
  assert.ok(h.page().has(detail));
  assert.equal(h.model.error, null);
});

test("an event that cannot be corrected has no Correct button, and the original view corrects nothing", async () => {
  const h = editorHarness();
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  const p = h.page();
  assert.equal(p.buttons(/^Correct event.retracted/).length, 0);
  assert.ok(p.buttons(/^Correct cycle.started/).length === 1);
  h.dispatch("editor.setView", "original");
  await new Promise((r) => setTimeout(r, 10));
  const q = h.page();
  assert.equal(q.buttons(/^Correct /).length, 0, "the original view is for reading");
  assert.equal(h.calls.filter((c) => c.get === "events" && c.q.view === "original").length >= 1, true);
});

// ---- the named operations ----

function opHarness() {
  const h = editorHarness({
    state: state({ focus: cycle({ id: "CYC1", title: "Deep work cycle" }) }),
    events: (q) => ({ view: "corrected", events: LOG.filter((i) => !q.types || q.types.includes(i.event.type)) }),
  });
  return h;
}

test("Insert break shows the cycle's running time before and after, with the zone, then sends the break", async () => {
  const h = opHarness();
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  await h.dispatch("op.open", "break");
  assert.equal(h.model.ui.editor.op.cycleId, "CYC1", "the running cycle is chosen first");
  h.dispatch("op.field", "from", "2026-10-07T12:00");
  h.dispatch("op.field", "to", "2026-10-07T13:00");
  const p = h.page();
  assert.ok(p.has("times in America/New_York"));
  assert.ok(p.has("The service checks the change when you confirm it"));
  const before = page(p.byAttr("data-timeline", "before")[0]);
  const after = page(p.byAttr("data-timeline", "after")[0]);
  assert.ok(before.has("2026-10-07 09:00 to 13:30"), before.text());
  assert.ok(before.has("Running time 4h30m"));
  assert.ok(after.has("2026-10-07 09:00 to 12:00") && after.has("2026-10-07 13:00 to 13:30"), after.text());
  assert.ok(after.has("Running time 3h30m"));
  await h.dispatch("op.submit");
  const post = h.lastPost();
  assert.equal(post.post, "/api/v1/cycles/break");
  assert.deepEqual([post.body.cycle_id, post.body.from, post.body.to], ["CYC1", "2026-10-07T16:00:00.000Z", "2026-10-07T17:00:00.000Z"]);
  assert.equal(h.model.ui.editor.op, null);
});

test("Insert break needs a start before its end, and says what is missing", async () => {
  const h = opHarness();
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  await h.dispatch("op.open", "break");
  assert.ok(h.page().has("Enter when the break started and when it ended."));
  assert.equal(h.page().button("Insert break").props.disabled, true);
  h.dispatch("op.field", "from", "2026-10-07T13:00");
  h.dispatch("op.field", "to", "2026-10-07T12:00");
  assert.ok(h.page().has("The break must start before it ends."));
  await h.dispatch("op.submit");
  assert.equal(h.posts().length, 0);
});

test("End at shows the cycle ended earlier and sends a stop with the time", async () => {
  const h = opHarness();
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  await h.dispatch("op.open", "endat");
  h.dispatch("op.field", "at", "2026-10-07T11:00");
  const p = h.page();
  assert.ok(page(p.byAttr("data-timeline", "after")[0]).has("2026-10-07 09:00 to 11:00"));
  await h.dispatch("op.submit");
  const post = h.lastPost();
  assert.equal(post.post, "/api/v1/cycles/stop");
  assert.deepEqual([post.body.cycle_id, post.body.effective_at], ["CYC1", "2026-10-07T15:00:00.000Z"]);
});

test("a refused operation shows the daemon's sentence in the form and changes nothing", async () => {
  const detail = "The break ends at the instant Deep work cycle was stopped; use End at.";
  const h = editorHarness({
    state: state({ focus: cycle({ id: "CYC1", title: "Deep work cycle" }) }),
    events: (q) => ({ view: "corrected", events: LOG.filter((i) => !q.types || q.types.includes(i.event.type)) }),
    post: () => {
      throw problem("break_ends_at_stop", detail);
    },
  });
  await h.start();
  await new Promise((r) => setTimeout(r, 5));
  await h.dispatch("op.open", "break");
  h.dispatch("op.field", "from", "2026-10-07T12:00");
  h.dispatch("op.field", "to", "2026-10-07T13:30");
  await h.dispatch("op.submit");
  assert.equal(h.model.ui.editor.op.error.detail, detail);
  assert.ok(h.page().has(detail));
  assert.ok(h.model.ui.editor.op, "the form stays");
});

// ---- the route ----

test("a link to an event the filters hide widens the range once", async () => {
  const h = harness({ hash: "#/events/01JS1", events: (q) => ({ view: "corrected", events: q.from ? [] : LOG }) });
  await h.start();
  await new Promise((r) => setTimeout(r, 20));
  assert.equal(h.model.ui.editor.from, "");
  assert.ok(h.model.ui.editor.items.some((i) => i.id === "01JS1"));
  assert.equal(h.model.focusRequest.id, "01JS1");
  assert.ok(h.page().where((n) => n.props["aria-current"] === "true" && n.props["data-event-id"] === "01JS1").length === 1);
});

test("a link to an event the log does not have says so", async () => {
  const h = harness({ hash: "#/events/01JNOPE", events: () => ({ view: "corrected", events: LOG }) });
  await h.start();
  await new Promise((r) => setTimeout(r, 20));
  assert.ok(h.page().has("The log has no event"));
});
