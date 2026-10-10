// The page's own modules against a real daemon.
//
// internal/daemon/webui_e2e_test.go starts the real daemon (a fake clock, a temp
// store, a fake sound player), puts a recording proxy in front of it that
// validates every request and response against api/openapi.yaml, and runs this
// file under `node --test`. The page is the real one: app.mjs's `start`, the
// store, the controller and the views, drawn into a fake DOM, talking through
// the page's own API client and an event stream that reads the daemon's real
// server-sent events. It is a day in the operator's life, scripted.
//
// Not run by the unit glob (`*.test.mjs`): it needs the environment below.
//   PGTF_BASE     the daemon's address (through the validating proxy)
//   PGTF_CONTROL  a control server: GET /clock?at=<RFC 3339> sets the daemon's clock,
//                 GET /fault makes the next append's fsync fail (read-only mode)

import assert from "node:assert/strict";
import { after, test } from "node:test";
import { start } from "../assets/app.mjs";
import { newRoot } from "./fakedom.mjs";
import { findIn, page } from "./page.mjs";

const BASE = process.env.PGTF_BASE;
const CONTROL = process.env.PGTF_CONTROL;
if (!BASE || !CONTROL) throw new Error("PGTF_BASE and PGTF_CONTROL are required; this file runs under the Go test of the daemon");

const NY = "America/New_York";
const realFetch = globalThis.fetch;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// ---- the page's environment ----

/** Every request the page makes, to prove what it sent and where. */
const sent = [];
async function pageFetch(path, init = {}) {
  sent.push({ url: path, method: init.method ?? "GET", body: init.body ?? "", headers: init.headers ?? {} });
  return realFetch(BASE + path, init);
}

/** An EventSource over fetch: the daemon's real stream, parsed the way a browser parses it. */
class FetchEventSource {
  constructor(url) {
    this.listeners = {};
    this.readyState = 0;
    this.ctrl = new AbortController();
    this.run(url).catch(() => {});
  }
  addEventListener(name, fn) {
    (this.listeners[name] ??= []).push(fn);
  }
  emit(name, data = "") {
    for (const fn of this.listeners[name] ?? []) fn({ data });
  }
  close() {
    this.readyState = 2;
    this.ctrl.abort();
  }
  async run(url) {
    let res;
    try {
      res = await realFetch(BASE + url, { headers: { Accept: "text/event-stream", "X-Client": "web" }, signal: this.ctrl.signal });
    } catch {
      this.readyState = 2;
      this.emit("error");
      return;
    }
    if (!res.ok) {
      this.readyState = 2;
      this.emit("error");
      return;
    }
    this.readyState = 1;
    this.emit("open");
    const dec = new TextDecoder();
    let buf = "";
    for await (const chunk of res.body) {
      buf += dec.decode(chunk, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const block = buf.slice(0, i);
        buf = buf.slice(i + 2);
        let name = "message";
        let data = "";
        for (const line of block.split("\n")) {
          if (line.startsWith("event: ")) name = line.slice(7);
          else if (line.startsWith("data: ")) data += line.slice(6);
        }
        if (!block.startsWith(":")) this.emit(name, data);
      }
    }
  }
}

let clockMs = Date.parse("2026-10-07T12:50:00.000Z"); // 08:50 in New York
async function setClock(iso) {
  // The daemon's clock and the page's are two clocks. A read the page makes between the two moves
  // would pair one clock's time with the other's, so let the page's own refreshes (the stream
  // events of what it just did) finish, then move the daemon's clock and the page's together.
  await sleep(80);
  const r = await realFetch(`${CONTROL}/clock?at=${encodeURIComponent(iso)}`);
  assert.equal(r.status, 204, "the control server set the daemon's clock");
  clockMs = Date.parse(iso);
}

const { doc, root } = newRoot();
const hooks = { hash: null, visible: null };
const intervals = [];
const location = { hash: "" };
const app = start({
  document: doc,
  root,
  location,
  fetch: pageFetch,
  EventSource: FetchEventSource,
  setInterval: (fn) => intervals.push(fn),
  setTimeout: (fn) => fn,
  onHashChange: (fn) => (hooks.hash = fn),
  onVisible: (fn) => (hooks.visible = fn),
  now: () => clockMs,
  zone: NY,
  find: (kind, id) => {
    if (kind === "banners") return findIn(root, "id", "banners");
    if (kind === "main") return findIn(root, "id", "main");
    if (kind === "undo") return findIn(root, "data-undo", "true");
    return findIn(root, { task: "data-task-id", cycle: "data-cycle-id", event: "data-event-id" }[kind], id);
  },
});
const { store, controller } = app;
after(() => app.close()); // the open stream would keep node alive
const dispatch = controller.dispatch;
const model = store.model;
// Reading the page draws the model as it is now, so a read never races the page's own scheduled draw.
const P = () => {
  app.draw();
  return page(root.__vnodes);
};
const tickOnce = () => intervals[0]();

async function until(cond, what, ms = 4000) {
  const end = Date.now() + ms;
  for (;;) {
    let v;
    try {
      v = cond();
    } catch {
      v = false;
    }
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}\npage: ${P().text().slice(0, 600)}`);
    await sleep(5);
  }
}
/** Lets the page's own notifications and requests settle. */
const settle = async () => {
  await sleep(40);
  await until(() => model.busy === 0, "no request in flight");
};
const goto = async (hash) => {
  location.hash = hash;
  hooks.hash();
  await settle();
};

const ctx = {};
const REASON = "zq-sentinel-reason";
const NOTE = "zq-sentinel-note";
const LABEL = "zq-sentinel-label";
const STOP_REASON = "zq-sentinel-all";

// ---- the day ----

test("an empty log invites the operator to set up, and the stream is up", async () => {
  await until(() => model.loaded, "the first read");
  assert.ok(P().has("Set your day, week and sprint to begin."));
  assert.ok(P().has("Nothing is set up yet."));
  await until(() => model.conn === "live", "the event stream");
});

test("the period modal bootstraps the day, week and sprint after a preview", async () => {
  await dispatch("period.open");
  await until(() => model.ui.modal === "period", "the modal");
  const f = model.ui.period.form;
  assert.equal(f.tz, NY, "the zone comes from the browser");
  assert.deepEqual([f.kinds.day.on, f.kinds.week.on, f.kinds.sprint.on], [true, true, true]);
  assert.equal(f.kinds.day.start, "2026-10-07");
  assert.equal(f.kinds.week.end, "2026-10-13", "a week with no predecessor is seven days");
  assert.equal(f.kinds.sprint.end, "");
  await dispatch("period.preview");
  assert.ok(model.ui.period.problems.some((p) => /sprint's last day/.test(p)), "the page asks for the sprint's end before it asks the daemon");
  dispatch("period.field", "kinds.sprint.end", "2026-10-18");
  dispatch("period.field", "kinds.day.label", LABEL);
  await dispatch("period.preview");
  await until(() => model.ui.period.preview, "the preview");
  const p = P();
  assert.ok(p.has("Tasks the new periods will add"));
  assert.ok(p.has("Plan the day"), "the daemon's preview lists what would be added");
  assert.ok(p.has("No open tasks are left behind."));
  assert.equal(p.button("Confirm the change").props.disabled, false);
  await dispatch("period.confirm");
  await settle();
  assert.equal(model.ui.modal, null);
  assert.equal(model.state.initialized, true);
  assert.equal(model.state.profile, "normal");
});

test("the checklists show the tasks with their due times and zones, and what is next", async () => {
  const p = P();
  for (const t of ["Plan the day", "Post the plan for the day", "Post the end-of-day summary", "Write the weekly status update", "Do the capacity check and post planning input"]) assert.ok(p.has(t), t);
  assert.ok(p.has("due 09:00 America/New_York"));
  assert.ok(p.has("Day: 2026-10-07 (zq-sentinel-label), America/New_York"));
  assert.ok(p.has("Start of day") && p.has("End of day"));
  assert.match(p.text(), /Next: [A-Z].*, due 09:00 America\/New_York, in 10m/);
  assert.ok(p.has("No cycle is running. Start one below."));
  assert.ok(p.has("Profile: normal"));
});

test("Done is one action, offers an Undo, and Undo takes it back", async () => {
  const id = "day:2026-10-07:post-plan";
  const btn = `Done: Post the plan for the day`;
  await P().click(btn);
  await until(() => model.state.tasks.find((t) => t.id === id).status === "completed", "the task completed");
  assert.ok(model.undo, "an Undo is offered");
  assert.ok(P().has("Completed Post the plan for the day."));
  assert.ok(P().hasButton("Undo"));
  await P().click("Undo");
  await until(() => model.state.tasks.find((t) => t.id === id).status === "open", "the task open again");
  assert.match(model.notice.text, /^Undone: Completed Post the plan for the day/);
  await P().click(btn);
  await until(() => model.state.tasks.find((t) => t.id === id).status === "completed", "the task completed again");
});

test("Skip asks for a reason; the reason is shown and appears only in a request body", async () => {
  const id = "sprint:2026-10-07:capacity-check";
  await P().click("Skip: Do the capacity check and post planning input");
  assert.equal(P().button("Skip task").props.disabled, true, "a blank reason cannot be confirmed");
  P().type("Reason for skipping", REASON);
  await P().submitOf("Skip: Do the capacity check and post planning input");
  await until(() => model.state.tasks.find((t) => t.id === id).status === "skipped", "the task skipped");
  assert.ok(P().has(`Reason: ${REASON}`));
  await until(() => model.ui.reasons?.includes(REASON), "the recent reasons read back from the log");
  const withReason = sent.filter((r) => `${r.url}${JSON.stringify(r.headers)}`.includes(REASON));
  assert.deepEqual(withReason, [], "the reason is in no URL and no header");
});

test("Done at sends the chosen time, read in the zone the page names", async () => {
  await setClock("2026-10-07T13:00:00.000Z");
  tickOnce();
  const t = model.state.tasks.find((x) => x.definition === "weekly-update");
  await P().click("Done at a time: Write the weekly status update");
  assert.ok(P().has("Done at (time in America/New_York)"));
  assert.equal(model.ui.taskForm.at, "2026-10-07T09:00:00", "it defaults to now");
  P().type("Done at (time in America/New_York)", "2026-10-07T08:55:00");
  await P().submitOf("Done at a time: Write the weekly status update");
  await until(() => model.state.tasks.find((x) => x.id === t.id)?.status === "completed", "completed at a chosen time");
  assert.equal(model.state.tasks.find((x) => x.id === t.id).resolved_at, "2026-10-07T12:55:00.000Z");
  assert.ok(P().has("Done 08:55 America/New_York"));
});

test("a cycle starts from the form, runs, pauses, and a repeated pause is a quiet no-op", async () => {
  await setClock("2026-10-07T13:00:00.000Z");
  P().type("Type", "deep-work");
  P().type("Minutes", "30");
  await P().submitOf("Start a cycle");
  await until(() => model.state.focus, "the cycle running");
  const dw = model.state.focus;
  assert.equal(dw.planned_minutes, 30);
  assert.equal(page(P().where((n) => n.props.role === "timer")[0]).text(), "30:00");
  assert.equal(doc.title, "Deep 30:00", "the tab title shows the timer");
  assert.ok(P().hasButton("Pause Deep work cycle"));

  await setClock("2026-10-07T13:10:00.000Z");
  await P().click("Pause Deep work cycle");
  await until(() => !model.state.focus && model.state.dimmed.length === 1, "the cycle paused");
  assert.ok(P().has("Paused"));
  assert.ok(P().hasButton("Resume Deep work cycle"), "with nothing running a paused cycle offers Resume");

  const before = model.state.version;
  await dispatch("cycle.pause", dw); // already paused: a no-op
  await settle();
  assert.equal(model.error, null, "no error for a pause of a paused cycle");
  assert.match(model.notice.text, /has been paused since/);
  assert.deepEqual(model.state.version, before, "and nothing changed");

  await setClock("2026-10-07T13:20:00.000Z");
  await P().click("Resume Deep work cycle");
  await until(() => model.state.focus?.id === dw.id, "resumed");
  ctx.dw = dw;
});

test("a cycle that interrupts another leaves it visible and dimmed, and the way back is offered until taken", async () => {
  const dw = ctx.dw;
  await setClock("2026-10-07T13:30:00.000Z");
  P().type("Type", "review");
  await P().submitOf("Start a cycle");
  await until(() => model.state.focus?.type === "review", "the review cycle");
  const review = model.state.focus;
  assert.equal(model.state.dimmed.length, 1);
  assert.equal(model.state.dimmed[0].id, dw.id);
  const li = P().byAttr("data-cycle-id", dw.id).find((n) => n.tag === "li");
  assert.ok(page(li).has("Paused") && page(li).has("Interrupted by Review cycle"));
  assert.ok(P().hasButton("Switch to Deep work cycle") && P().hasButton("Stop Deep work cycle"));

  await setClock("2026-10-07T13:35:00.000Z");
  await P().click("Stop Review cycle");
  await until(() => !model.state.focus, "the interrupting cycle stopped");
  assert.ok(model.undo, "stopping offers an Undo");
  await until(() => model.state.resume_offer, "the resume offer");
  assert.ok(P().has("Resume Deep work cycle?"));
  assert.ok(P().hasButton("Resume Deep work cycle (offered)"));

  await setClock("2026-10-07T13:36:00.000Z");
  P().type("Type", "review");
  await P().submitOf("Start a cycle");
  await until(() => model.state.focus, "a second review cycle");
  assert.equal(model.state.resume_offer?.action, "switch", "while another cycle runs the offer is Switch");
  assert.ok(P().hasButton("Switch to Deep work cycle (offered)"));

  await setClock("2026-10-07T13:40:00.000Z");
  await P().click("Switch to Deep work cycle (offered)");
  await until(() => model.state.focus?.id === dw.id, "switched");
  assert.equal(model.state.dimmed[0].type, "review", "the one that ran is dimmed");
  assert.equal(model.state.resume_offer, null, "the offer ends when its cycle is switched to");
  ctx.review = review;
});

test("a note and key/value pairs are saved whole, pre-filled from the type's keys, and a boost adds minutes", async () => {
  const dw = model.state.focus;
  const keys = P().where((n) => n.tag === "input" && n.props.pattern === "[a-z0-9_-]+" && n.props.id.startsWith(`kv-key-${dw.id}`)).map((n) => n.props.value);
  assert.deepEqual(keys, ["ticket", "pr"], "the keys of the type are pre-filled");
  dispatch("note.field", dw, NOTE);
  dispatch("note.kv", dw, 0, "value", "ABC-1");
  await dispatch("note.save", dw);
  await until(() => model.state.focus.note === NOTE, "the note saved");
  assert.deepEqual(model.state.focus.kv, [{ key: "ticket", value: "ABC-1" }], "the empty pair was not sent");
  await P().click("Boost Deep work cycle by 10 minutes");
  await until(() => model.state.focus.boost_minutes === 10, "boosted");
  assert.ok(P().has("Planned 30m, boosted 10m"));
});

test("a timer runs on the page's clock, and overtime is a negative time that is announced once", async () => {
  // Elapsed 10 + 10 + 25 = 45 minutes by 10:05, past planned 30 + boost 10.
  await setClock("2026-10-07T14:05:00.000Z");
  tickOnce();
  await sleep(10);
  const timer = page(P().where((n) => n.props.role === "timer")[0]).text();
  assert.match(timer, /^\+05:00$/, "the page counted from the daemon's read on its own clock");
  assert.equal(doc.title, "Deep +05:00");
  assert.match(model.announce.polite.items.at(-1).text, /Deep work cycle is out of time/);
  const seq = model.announce.polite.seq;
  tickOnce();
  tickOnce();
  assert.equal(model.announce.polite.seq, seq, "not every second");
  // The daemon agrees.
  await store.refresh();
  await sleep(10);
  assert.equal(model.state.focus.remaining_seconds, -300);
  assert.equal(page(P().where((n) => n.props.role === "timer")[0]).text(), "+05:00");
});

test("a change made by another client appears without polling", async () => {
  // As the command line would: a direct request to the daemon.
  const before = JSON.stringify(model.state.version);
  const r = await realFetch(`${BASE}/api/v1/cycles/boost`, { method: "POST", headers: { "Content-Type": "application/json", "X-Client": "cli" }, body: JSON.stringify({ cycle_id: model.state.focus.id, minutes: 5 }) });
  assert.equal(r.status, 200);
  await until(() => JSON.stringify(model.state.version) !== before && model.state.focus.boost_minutes === 15, "the page followed the stream");
  assert.ok(P().has("boosted 15m"));
  assert.equal(model.conn, "live");
});

test("a refusal shows the daemon's own sentence, byte for byte, with its code and trace id", async () => {
  const done = model.state.tasks.find((t) => t.status === "completed");
  await dispatch("task.done", done);
  await settle();
  const e = model.error;
  assert.equal(e.reason, "task_already_resolved");
  assert.ok(e.traceId.length >= 16);
  // The same request made directly gets the same sentence.
  const direct = await realFetch(`${BASE}/api/v1/tasks/${encodeURIComponent(done.id)}/complete`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
  const problem = await direct.json();
  assert.equal(direct.status, 409);
  assert.equal(e.detail, problem.detail, "the page's sentence is the daemon's, unchanged");
  assert.ok(P().has(problem.detail));
  assert.ok(P().has(`trace ${e.traceId}`));
  dispatch("error.dismiss");
});

test("a verb the daemon cannot place is a choice among the cycles it names, never a guess", async () => {
  // Two cycles are not stopped: the running deep-work and the dimmed review.
  assert.equal(model.state.dimmed.length, 1);
  await store.perform({ path: "/api/v1/cycles/resume", body: {}, label: "Resume without naming a cycle" });
  const e = model.error;
  assert.equal(e.reason, "cycle_ambiguous");
  assert.deepEqual(e.candidates.map((c) => c.title).sort(), ["Deep work cycle", "Review cycle"]);
  assert.ok(P().has("Which cycle do you mean?"));
  await P().click("Use Deep work cycle"); // it already runs: a no-op, not an error
  await settle();
  assert.equal(model.error, null);
  assert.ok(model.notice, "a quiet note says why nothing changed");
  // The page's own controls always named the cycle; the one request that did not is the one above.
  const unnamed = sent.filter((r) => r.method === "POST" && /\/cycles\/(pause|resume|stop|boost|switch|annotate)$/.test(r.url) && !("cycle_id" in JSON.parse(r.body)) && !("to" in JSON.parse(r.body)));
  assert.equal(unnamed.length, 1);
});

// ---- a new day while cycles are not stopped ----

test("a period change is blocked while a cycle is running or paused, with Stop and End at for each", async () => {
  await setClock("2026-10-08T13:00:00.000Z"); // 09:00 the next day
  tickOnce();
  await store.refresh();
  await until(() => model.state.periods.find((p) => p.kind === "day").ended, "the day has ended");
  assert.ok(P().hasButton("New day: roll over"), "the exact banner string is the action");
  assert.ok(model.announce.polite.items.some((i) => i.text === "New day: roll over"), "the ended day is announced");
  assert.equal(P().buttons("New day: roll over").length, 1);

  await P().click("New day: roll over");
  await until(() => model.ui.modal === "period", "the modal");
  assert.equal(model.ui.period.form.kinds.day.start, "2026-10-08");
  assert.equal(model.ui.period.form.kinds.week.on, false, "only the periods that ended are chosen");
  let p = P();
  assert.ok(p.has("A cycle is not stopped"));
  assert.ok(p.hasButton("Stop Deep work cycle now") && p.hasButton("Stop Review cycle now"));
  assert.equal(p.control("End Deep work cycle at (America/New_York)").props.value, "", "End at has no prefill");
  await dispatch("period.preview");
  await until(() => model.ui.period.preview, "the preview");
  p = P();
  assert.equal(p.button("Confirm the change").props.disabled, true, "confirmation is blocked");
  assert.ok(p.has("Open tasks in the periods being left"), "the preview is shown in full");
  // The daemon refuses the change too, in its own words, inside the modal.
  const refused = await realFetch(`${BASE}/api/v1/periods/change`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ changes: [{ kind: "day", start: "2026-10-08", tz: NY }] }) });
  assert.equal((await refused.json()).reason, "cycle_active");

  // End the running cycle at the time it really ended, and stop the other now.
  await dispatch("blocker.endAt", model.state.focus);
  assert.match(model.ui.period.endAtProblem[model.state.focus.id], /really ended/, "End at needs a time");
  dispatch("blocker.endAtField", model.state.focus.id, "2026-10-07T10:05");
  await dispatch("blocker.endAt", model.state.focus);
  await until(() => !model.state.focus, "the focus ended");
  assert.equal(model.undo, null, "no Undo is offered behind the dialog");
  await P().click("Stop Review cycle now");
  await until(() => model.state.dimmed.length === 0 && !model.state.focus, "no cycle is left");
  await until(() => model.ui.period.preview && model.ui.period.preview.result.preview.blocking_cycles.length === 0, "the preview was read again");
  assert.equal(P().button("Confirm the change").props.disabled, false);
  assert.equal(model.ui.modal, "period");
});

test("skip all with one reason, one task with its own, confirm with the preview's version", async () => {
  const leaving = model.ui.period.preview.result.preview.leaving;
  assert.ok(leaving.length >= 2, `open tasks are left behind: ${JSON.stringify(leaving)}`);
  await P().click("Skip all with one reason");
  assert.equal(P().button("Confirm the change").props.disabled, true, "the one reason is required");
  P().type("Reason for skipping all of them", STOP_REASON);
  const own = leaving[0];
  ctx.oldTask = own.id;
  dispatch("rollover.task", own.id, true, undefined);
  dispatch("rollover.task", own.id, undefined, "its own reason");
  await sleep(20);
  assert.ok(P().has("→ will be skipped"));
  await dispatch("period.confirm");
  await settle();
  assert.equal(model.ui.modal, null);
  const day = model.state.periods.find((p) => p.kind === "day");
  assert.equal(day.start, "2026-10-08");
  assert.equal(day.ended, false);
  assert.ok(model.undo, "the change can be undone");
  assert.ok(P().has("Undo is refused once later events depend on it"));
  // What the daemon recorded for the old tasks.
  const res = await (await realFetch(`${BASE}/api/v1/events?type=task.skipped`)).json();
  const skipped = res.events.filter((i) => !i.retracted).map((i) => i.event.data);
  const own_ = skipped.find((d) => d.task_id === own.id);
  assert.equal(own_.reason, "its own reason");
  assert.ok(skipped.some((d) => d.reason === STOP_REASON));
  assert.equal(sent.filter((r) => r.url.includes(STOP_REASON)).length, 0);
});

// ---- the editor ----

test("the editor lists the corrected log, with the original one action away", async () => {
  await goto("#/events");
  await until(() => model.ui.editor?.items, "the events");
  const p = P();
  assert.ok(p.has("Nothing is ever deleted"));
  assert.ok(p.has("Started: Deep work cycle (deep-work), 30 min planned"));
  assert.ok(p.has("Skipped: Do the capacity check and post planning input (reason: zq-sentinel-reason)"));
  assert.ok(p.has("Boosted: Deep work cycle by 10 min"));
  assert.equal(p.where((n) => n.tag === "button" && /delete/i.test(n.props["aria-label"] ?? "")).length, 0);
  assert.ok(p.hasButton("Pause Deep work cycle") === false, "no cycle runs");
  assert.ok(P().has("Cycles"), "the cycle panel is in the editor area too");
});

test("fixing a cycle's type: the title follows from the daemon, and the original stays one click away", async () => {
  const items = model.ui.editor.items;
  const review = items.find((i) => i.event.type === "cycle.started" && i.event.data.type === "review");
  dispatch("editor.openCorrect", review);
  await sleep(20);
  assert.equal(P().control("Type").tag, "select");
  dispatch("editor.correctField", "type", "notifications");
  await dispatch("editor.submitCorrect");
  await until(() => model.ui.editor.correct === null, "the correction accepted");
  await until(() => {
    const it = model.ui.editor.items.find((i) => i.id === review.id);
    return it?.event.data.type === "notifications" && it.corrected_by.length === 1;
  }, "the corrected view");
  const fixed = model.ui.editor.items.find((i) => i.id === review.id);
  assert.equal(fixed.event.data.title, "Notification cycle", "the title followed the type");
  assert.equal(fixed.original.data.type, "review");
  dispatch("editor.toggleOriginal", review.id);
  await sleep(10);
  assert.ok(P().has("As first logged:") && P().has("Review cycle (review)"));
});

test("Insert break shows the cycle's running time before and after, then the daemon accepts it", async () => {
  const items = model.ui.editor.items;
  const dwStart = items.find((i) => i.event.type === "cycle.started" && i.event.data.type === "deep-work");
  const id = dwStart.event.data.cycle_id;
  await dispatch("op.open", "break");
  await until(() => model.ui.editor.cycleItems, "the cycles");
  dispatch("op.field", "cycleId", id);
  dispatch("op.field", "from", "2026-10-07T09:50");
  dispatch("op.field", "to", "2026-10-07T10:00");
  await sleep(10);
  const p = P();
  const before = page(p.byAttr("data-timeline", "before")[0]);
  const after = page(p.byAttr("data-timeline", "after")[0]);
  assert.ok(before.has("Running time 45m"), before.text());
  assert.ok(after.has("Running time 35m"), after.text());
  assert.ok(after.has("2026-10-07 09:40 to 09:50") && after.has("2026-10-07 10:00 to 10:05"), after.text());
  assert.ok(p.has("The service checks the change when you confirm it"));
  await dispatch("op.submit");
  await until(() => model.ui.editor.op === null, "the break accepted");
  ctx.dwId = id;
});

test("a break the daemon refuses is shown in its words and changes nothing", async () => {
  await dispatch("op.open", "break");
  await until(() => model.ui.editor.cycleItems, "the cycles");
  dispatch("op.field", "cycleId", ctx.dwId);
  dispatch("op.field", "from", "2026-10-07T09:52");
  dispatch("op.field", "to", "2026-10-07T09:58"); // inside the break just inserted
  await dispatch("op.submit");
  await until(() => model.ui.editor.op?.error, "the refusal");
  assert.equal(model.ui.editor.op.error.reason, "cycle_segments_overlap");
  assert.ok(P().has(model.ui.editor.op.error.detail));
  assert.ok(model.ui.editor.op, "the form stays");
  dispatch("op.cancel");
});

test("a retraction the daemon refuses names the events it depends on, as links into the editor", async () => {
  // The day change has dependents: its tasks are referenced by later events? Complete a new-day task first.
  const t = model.state.tasks.find((x) => x.kind === "day" && x.status === "open");
  await dispatch("task.done", t);
  await settle();
  await until(() => model.ui.editor.items.some((i) => i.event.type === "task.completed" && i.event.data.task_id === t.id), "the completion listed");
  const batchItem = model.ui.editor.items.find((i) => i.event.type === "task.materialized" && i.event.data.task_id === t.id);
  await dispatch("editor.retract", batchItem);
  await until(() => model.ui.editor.rowError, "the refusal");
  const rec = model.ui.editor.rowError.record;
  assert.equal(rec.reason, "batch_has_dependents");
  const links = P().where((n) => n.tag === "a" && n.props.href?.startsWith("#/events/")).map((n) => n.props.href);
  assert.ok(links.length >= 1, "the dependents are links");
  assert.ok(P().has(rec.detail));
  assert.equal(model.error, null);
  dispatch("editor.dismissRowError");
});

// ---- deep links ----

test("deep links: a stopped cycle and a task of an earlier day show the log's account; the current ones are focused", async () => {
  await goto(`#/cycles/${ctx.dwId}`);
  await until(() => P().byAttr("data-detail", "cycle").length === 1, "the cycle's detail");
  assert.ok(P().has("Stopped at 2026-10-07 10:05 America/New_York"));
  assert.ok(P().has("Running time 35m"), "the break reduced it");
  assert.ok(P().has(`Note: ${NOTE}`));
  assert.ok(P().has("ticket: ABC-1"));

  await goto(`#/tasks/${ctx.oldTask}`);
  await until(() => P().byAttr("data-detail", "task").length === 1, "the task's detail");
  assert.ok(P().has("Its last known state in the log: skipped."));

  const cur = model.state.tasks.find((x) => x.kind === "day");
  await goto(`#/tasks/${cur.id}`);
  await until(() => doc.activeElement?.getAttribute("data-task-id") === cur.id, "the task focused");
  assert.equal(P().byAttr("data-detail", "task").length, 0);

  await goto("#/tasks/day:1999-01-01:nothing");
  await until(() => P().has("The log has no task with the id day:1999-01-01:nothing."), "the unknown id said in words");
  assert.ok(P().has(cur.title), "the rest of the page is intact");
  await goto("#/");
});

// ---- the page asks nothing of the operator's words ----

test("the operator's words left the page only as request bodies", async () => {
  for (const w of [REASON, NOTE, LABEL, STOP_REASON, "its own reason"]) {
    const where = sent.filter((r) => r.url.includes(w) || JSON.stringify(r.headers).includes(w));
    assert.deepEqual(where.map((r) => r.url), [], `${w} appears in a URL or a header`);
    assert.ok(sent.some((r) => r.body.includes(w)), `${w} was sent in a body, as it must be`);
  }
  assert.ok(sent.every((r) => r.url.startsWith("/api/v1/")), "every request is to the API: " + [...new Set(sent.map((r) => r.url.split("?")[0]))].join(" "));
  const mutations = sent.filter((r) => r.method === "POST" && !JSON.parse(r.body).dry_run);
  assert.ok(mutations.length > 10);
  assert.ok(mutations.every((r) => /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(JSON.parse(r.body).id ?? "")), "every mutation carries a ULID id");
});

// ---- read-only mode ----

test("when the store becomes read-only the page says so at once, at the top, and disables every mutating control", async () => {
  await goto("#/");
  await setClock("2026-10-08T13:10:00.000Z");
  P().type("Type", "review");
  await P().submitOf("Start a cycle");
  await until(() => model.state.focus, "a cycle to work with");
  const focus = model.state.focus;
  assert.equal(P().byAttr("data-banner", "read-only").length, 0);
  // The next append's fsync fails: the outcome is unknown and the store goes read-only.
  assert.equal((await realFetch(`${CONTROL}/fault`)).status, 204);
  await setClock("2026-10-08T13:20:00.000Z");
  await P().click("Pause Review cycle");
  await settle();
  await until(() => model.state.store.state === "read_only", "the page read the mode");
  const banner = P().byAttr("data-banner", "read-only")[0];
  assert.ok(banner, "the banner is up");
  assert.equal(page(banner).text(), "READ-ONLY: the append fsync failed. Restart pg-task-focus to recover");
  assert.equal(banner.props.role, "alert");
  assert.equal(P().where((n) => n.props.id === "banners")[0].children[0].props["data-banner"], "read-only", "it is first, in the sticky banners");
  assert.equal(model.error.readOnly, true);
  assert.equal(model.error.retryable, false, "a repeat would not help");
  assert.equal(doc.title.startsWith("READ-ONLY"), true);
  const mutators = P().mutators();
  assert.ok(mutators.length >= 8, `the page has mutating controls: ${mutators.length}`);
  for (const n of mutators) assert.equal(n.props.disabled, true, `${n.tag} ${n.props["aria-label"] ?? n.props.id}`);
  // The editor is a different area, and has the banner and the disabled controls too.
  await goto("#/events");
  await until(() => model.ui.editor.items, "the editor still reads");
  assert.equal(P().byAttr("data-banner", "read-only").length, 1);
  for (const n of P().mutators()) assert.equal(n.props.disabled, true);
  const reads = P().buttons(/^(Show|Hide) the original/);
  assert.ok(reads.length > 0 && reads.every((b) => !b.props.disabled), "reading still works");
  // A request the page makes anyway is refused with the same sentence.
  await dispatch("cycle.stop", focus);
  assert.ok(model.error.detail.startsWith("READ-ONLY: the append fsync failed"));
});
