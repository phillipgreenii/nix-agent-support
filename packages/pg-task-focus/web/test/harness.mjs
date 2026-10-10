// A store and a controller over a fake daemon API, for the tests of the page's
// behavior: what a press does, which request it makes, what the page shows
// after the answer. The fake records every call.

import { ApiError } from "../assets/api.mjs";
import { createController } from "../assets/intents.mjs";
import { createStore } from "../assets/store.mjs";
import { viewApp } from "../assets/view-app.mjs";
import { ULID_PATTERN } from "../assets/ulid.mjs";
import { config, state as stateFixture, T0 } from "./fixtures.mjs";
import { page } from "./page.mjs";

/** The messages assistive technology was told, newest last. */
export const said = (h, kind = "polite") => h.model.announce[kind].items.map((i) => i.text);

export const tick = () => new Promise((r) => setTimeout(r, 0));

/** A problem as the daemon sends it, as an ApiError. */
export function problem(reason, detail, extra = {}) {
  return new ApiError({ kind: "problem", status: extra.status ?? 409, reason, detail, traceId: "trace1", details: extra.details ?? null, store: extra.store ?? null });
}

export function networkError() {
  return new ApiError({ kind: "network", detail: "The service could not be reached, so the outcome of this request is unknown." });
}

/**
 * @param {object} [o]
 * @param {object} [o.state] the first state
 * @param {(path: string, body: object, calls: object[]) => object | Promise<object>} [o.post] answers a POST; throw an ApiError to fail
 * @param {(q: object) => object} [o.events] answers GET /events
 */
export function harness({ state = stateFixture(), post, events, hash = "" } = {}) {
  const calls = [];
  let current = state;
  let nowMs = T0;
  let n = 0;
  const api = {
    calls,
    async getState() {
      calls.push({ get: "state" });
      return structuredClone(current);
    },
    async getConfig() {
      calls.push({ get: "config" });
      return structuredClone(config);
    },
    async getEvents(q = {}) {
      calls.push({ get: "events", q });
      return events ? structuredClone(events(q)) : { view: q.view ?? "corrected", events: [] };
    },
    async post(path, body) {
      calls.push({ post: path, body: structuredClone(body) });
      if (!post) return { changed: true, event_ids: ["01JNEWEVENT0000000000000A"], version: current.version };
      return structuredClone(await post(path, body, calls));
    },
  };
  const store = createStore({
    api,
    now: () => nowMs,
    newId: () => `01JID${String(++n).padStart(21, "0")}`.slice(0, 26),
    browserZone: "America/New_York",
    hash,
  });
  const controller = createController(store);
  return {
    api,
    calls,
    store,
    controller,
    model: store.model,
    dispatch: controller.dispatch,
    /** Sets the state the fake daemon answers with next. */
    setState(s) {
      current = s;
    },
    currentState: () => current,
    clock: (ms) => {
      nowMs = ms;
    },
    advance(ms) {
      nowMs += ms;
      store.tick(nowMs);
    },
    async start() {
      await controller.init();
      await tick();
    },
    page: () => page(viewApp(store.model, controller.dispatch)),
    posts: () => calls.filter((c) => c.post),
    lastPost: () => calls.filter((c) => c.post).at(-1),
    ids: () => calls.filter((c) => c.post).map((c) => c.body.id),
  };
}

export { ULID_PATTERN };
