import assert from "node:assert/strict";
import { test } from "node:test";
import { start } from "../assets/app.mjs";
import { openStream } from "../assets/stream.mjs";
import { config, cycle, dimmed, state, T0 } from "./fixtures.mjs";
import { newRoot } from "./fakedom.mjs";
import { findIn, page } from "./page.mjs";
import { harness } from "./harness.mjs";

const wait = (ms = 10) => new Promise((r) => setTimeout(r, ms));

// ---- the stream ----

class FakeES {
  static all = [];
  constructor(url) {
    this.url = url;
    this.readyState = 0;
    this.listeners = {};
    this.closed = false;
    FakeES.all.push(this);
  }
  addEventListener(n, fn) {
    (this.listeners[n] ??= []).push(fn);
  }
  emit(n, data) {
    for (const fn of this.listeners[n] ?? []) fn({ data });
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
}

test("the stream says something changed on a state or store event, and when it is up or lost", () => {
  FakeES.all = [];
  const log = [];
  const close = openStream({ EventSource: FakeES, url: "/api/v1/stream", onChange: () => log.push("change"), onOpen: () => log.push("open"), onLost: () => log.push("lost") });
  const es = FakeES.all[0];
  assert.equal(es.url, "/api/v1/stream");
  es.emit("open");
  es.emit("state", '{"version":{}}');
  es.emit("store", '{"state":"read_only"}');
  es.emit("error");
  assert.deepEqual(log, ["open", "change", "change", "lost"]);
  close();
  assert.equal(es.closed, true);
});

test("a stream the browser closed for good (a 503 while the daemon starts) is opened again after a pause", () => {
  FakeES.all = [];
  const timers = [];
  const log = [];
  openStream({ EventSource: FakeES, url: "/s", onChange() {}, onOpen: () => log.push("open"), onLost: () => log.push("lost"), setTimeout: (fn, ms) => timers.push([fn, ms]), reopenMs: 1234 });
  const first = FakeES.all[0];
  first.readyState = 2; // CLOSED: the browser will not retry
  first.emit("error");
  assert.equal(timers.length, 1);
  assert.equal(timers[0][1], 1234);
  timers[0][0]();
  assert.equal(FakeES.all.length, 2, "a new connection was made");
  // A browser that is retrying by itself (CONNECTING) is left alone.
  const second = FakeES.all[1];
  second.readyState = 0;
  second.emit("error");
  assert.equal(timers.length, 1);
});

test("a stream that was closed on purpose is not reopened", () => {
  FakeES.all = [];
  const timers = [];
  const close = openStream({ EventSource: FakeES, url: "/s", onChange() {}, onOpen() {}, onLost() {}, setTimeout: (fn) => timers.push(fn) });
  close();
  FakeES.all[0].emit("error");
  assert.equal(timers.length, 0);
});

// ---- the glue ----

function boot(over = {}) {
  FakeES.all = [];
  const { doc, root } = newRoot();
  const location = { hash: over.hash ?? "" };
  const h = harness({ state: over.state });
  const intervals = [];
  const hooks = { hash: null, visible: null };
  let nowMs = T0;
  const env = {
    document: doc,
    root,
    location,
    fetch: async (path, init) => {
      // The page's own API client against a fake daemon.
      const json = (body, status = 200) => ({ ok: status < 300, status, headers: new Map(), text: async () => JSON.stringify(body) });
      if (path === "/api/v1/state") return json(h.currentState());
      if (path === "/api/v1/config") return json(config);
      if (path.startsWith("/api/v1/events")) return json({ view: "corrected", events: [] });
      return json({ changed: true, event_ids: ["01JE"], version: h.currentState().version });
    },
    EventSource: FakeES,
    setInterval: (fn, ms) => intervals.push([fn, ms]),
    setTimeout: (fn) => fn,
    onHashChange: (fn) => (hooks.hash = fn),
    onVisible: (fn) => (hooks.visible = fn),
    now: () => nowMs,
    zone: "America/New_York",
    find: (kind, id) => {
      if (kind === "banners") return findIn(root, "id", "banners");
      if (kind === "main") return findIn(root, "id", "main");
      if (kind === "undo") return findIn(root, "data-undo", "true");
      return findIn(root, { task: "data-task-id", cycle: "data-cycle-id", event: "data-event-id" }[kind], id);
    },
  };
  let current = over.state ?? state();
  h.currentState = () => current;
  const app = start(env);
  return { app, doc, root, env, intervals, hooks, FakeES, setState: (s) => (current = s), setNow: (ms) => (nowMs = ms), location };
}

test("the page draws, reads the state and sets the tab title to the timer", async () => {
  const b = boot({ state: state({ focus: cycle({ remaining_seconds: 750 }) }) });
  assert.match(b.root.textContent, /Reading the state/);
  await wait(30);
  assert.equal(b.doc.title, "Deep 12:30");
  assert.match(b.root.textContent, /Pause Deep work cycle|Pause/);
  b.setNow(T0 + 5000);
  b.intervals[0][0](); // the one-second tick
  await wait(5);
  assert.equal(b.doc.title, "Deep 12:25");
  assert.equal(b.root.byTag("button").length > 5, true);
});

test("a stream event reads the state again, with no polling while the stream is up", async () => {
  const b = boot();
  await wait(30);
  const es = FakeES.all[0];
  es.emit("open");
  await wait(10);
  b.setState(state({ focus: cycle(), version: { log_lines: 41, config_generation: 1 } }));
  es.emit("state", "{}");
  await wait(20);
  assert.match(b.root.textContent, /Running/, "a change made elsewhere appears");
  // The fallback poll only runs while the stream is down.
  const requests = [];
  const orig = b.env.fetch;
  b.env.fetch = async (...a) => (requests.push(a[0]), orig(...a));
  const poll = b.intervals[1][0];
  for (let i = 0; i < 6; i++) poll();
  await wait(10);
  assert.equal(requests.length, 0);
  es.emit("error");
  await wait(5);
  assert.match(b.root.textContent, /The connection to the service was lost/);
});

test("coming back to the tab reads the state again, which is what keeps a timer right after sleep", async () => {
  const b = boot({ state: state({ focus: cycle({ remaining_seconds: 100 }) }) });
  await wait(30);
  b.setState(state({ focus: cycle({ remaining_seconds: -40 }), version: { log_lines: 50, config_generation: 1 } }));
  b.setNow(T0 + 3600000);
  b.hooks.visible();
  await wait(30);
  assert.equal(b.doc.title, "Deep +00:40");
});

test("a hash change is a route change, and a deep link focuses the task it names", async () => {
  const b = boot();
  await wait(30);
  b.location.hash = "#/tasks/day:2026-10-07:post-plan";
  b.hooks.hash();
  await wait(20);
  assert.equal(b.doc.activeElement?.getAttribute("data-task-id"), "day:2026-10-07:post-plan", "the task is focused");
  assert.equal(b.doc.activeElement.getAttribute("aria-current"), "true");
});

test("a deep link on first load, to a cycle in the panel, focuses it", async () => {
  const b = boot({ hash: `#/cycles/${dimmed().id}`, state: state({ focus: cycle(), dimmed: [dimmed()] }) });
  await wait(40);
  assert.equal(b.doc.activeElement?.getAttribute("data-cycle-id"), dimmed().id);
});

test("the skip link moves focus to the main area", async () => {
  const b = boot();
  await wait(30);
  const skip = b.root.byTag("button")[0];
  skip.onclick();
  await wait(10);
  assert.equal(b.doc.activeElement?.getAttribute("id"), "main");
});

test("after an action that removes the control the operator was on, focus goes to the Undo", async () => {
  const b = boot();
  await wait(30);
  const done = b.root.byTag("button").find((x) => x.getAttribute("aria-label") === "Done: Post the plan for the day");
  done.focus();
  assert.equal(b.doc.activeElement, done);
  b.doc.activeElement = null; // the click re-rendered the row and the focused button went with it
  done.onclick();
  await wait(40);
  assert.equal(b.doc.activeElement?.getAttribute("data-undo"), "true");
});
