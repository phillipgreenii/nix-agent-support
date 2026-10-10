// The browser glue, the only module that touches the DOM, the clock, the
// location and EventSource. Everything it starts is built from modules that run
// under node, so `start` takes its environment as an argument and the page's
// own load is the one call that passes the real one.

import { createApi } from "./api.mjs";
import { createController } from "./intents.mjs";
import { parseRoute, tabTitle } from "./select.mjs";
import { createStore } from "./store.mjs";
import { openStream } from "./stream.mjs";
import { cryptoRandom, ulid } from "./ulid.mjs";
import { render } from "./vdom.mjs";
import { viewApp } from "./view-app.mjs";
import { browserZone } from "./zone.mjs";

/**
 * @param {object} env
 * @param {Document} env.document
 * @param {Element} env.root the element the page renders into
 * @param {{hash: string}} env.location
 * @param {typeof fetch} env.fetch
 * @param {typeof EventSource | undefined} env.EventSource
 * @param {(fn: () => void, ms: number) => unknown} env.setInterval
 * @param {(fn: () => void, ms: number) => unknown} [env.setTimeout]
 * @param {() => number} [env.now]
 * @param {(fn: () => void) => void} env.onHashChange
 * @param {(fn: () => void) => void} env.onVisible called when the tab becomes visible
 * @param {(kind: string, id: string) => Element | null} env.find the element of a task, cycle or event, by its id
 * @param {(px: number) => void} [env.setStickyOffset]
 * @param {string} [env.zone]
 */
export function start(env) {
  const now = env.now ?? Date.now;
  const api = createApi({ fetch: env.fetch });
  const store = createStore({
    api,
    now,
    newId: () => ulid(now(), cryptoRandom),
    browserZone: env.zone ?? browserZone(),
    hash: env.location.hash,
  });
  const controller = createController(store);
  const { model } = store;
  let lastFocusSeq = 0;
  let lastUndoSeq = 0;

  function draw() {
    render(env.root, viewApp(model, controller.dispatch));
    env.document.title = tabTitle(model);
    applyFocus();
    const banners = env.find("banners", "");
    if (banners && env.setStickyOffset) env.setStickyOffset(banners.offsetHeight ?? 0);
  }

  // A route or an action asks for focus; it is given once, after the page has been drawn.
  function applyFocus() {
    const f = model.focusRequest;
    if (f && f.seq !== lastFocusSeq) {
      const el = f.kind === "main" ? env.find("main", "") : env.find(f.kind, f.id ?? "");
      if (el) {
        lastFocusSeq = f.seq;
        el.focus();
        el.scrollIntoView?.({ block: "center" });
      }
    }
    // An action that removed the control the operator was on (Done) leaves focus nowhere; the Undo is the next thing they need.
    if (model.undo && model.undo.seq !== lastUndoSeq) {
      lastUndoSeq = model.undo.seq;
      const active = env.document.activeElement;
      if (!active || active === env.document.body) env.find("undo", "")?.focus();
    }
  }

  store.subscribe(draw);
  draw();

  env.setInterval(() => store.tick(), 1000);
  let ticks = 0;
  env.setInterval(() => {
    // The stream is the way changes arrive; while it is down the page asks.
    ticks++;
    if (model.conn !== "live" && ticks % 5 === 0) store.refresh();
  }, 3000);
  env.onHashChange(() => store.setRoute(parseRoute(env.location.hash)));
  env.onVisible(() => {
    store.tick();
    store.refresh();
  });

  let closeStream = () => {};
  if (env.EventSource) {
    closeStream = openStream({
      EventSource: env.EventSource,
      url: "/api/v1/stream",
      setTimeout: env.setTimeout,
      onOpen: () => {
        store.setConn("live");
        store.refresh();
      },
      onChange: () => store.refresh(),
      onLost: () => store.setConn("lost"),
    });
  }
  controller.init();
  return { store, controller, draw, close: closeStream };
}

// The page's own load. Under node there is no window, so importing this module starts nothing.
if (typeof window !== "undefined" && window.document) {
  const doc = window.document;
  const sel = {
    banners: () => doc.getElementById("banners"),
    main: () => doc.getElementById("main"),
    undo: () => doc.querySelector('[data-undo="true"]'),
  };
  start({
    document: doc,
    root: doc.getElementById("app"),
    location: window.location,
    fetch: window.fetch.bind(window),
    EventSource: window.EventSource,
    setInterval: window.setInterval.bind(window),
    setTimeout: window.setTimeout.bind(window),
    onHashChange: (fn) => window.addEventListener("hashchange", fn),
    onVisible: (fn) => doc.addEventListener("visibilitychange", () => !doc.hidden && fn()),
    find: (kind, id) => {
      if (sel[kind]) return sel[kind]();
      const attr = { task: "data-task-id", cycle: "data-cycle-id", event: "data-event-id" }[kind];
      return attr ? doc.querySelector(`[${attr}="${window.CSS.escape(id)}"]`) : null;
    },
    setStickyOffset: (px) => doc.documentElement.style.setProperty("--banners-height", `${px}px`),
  });
}
