// The page's model and the few things every feature shares: reading the state,
// making a request with an idempotency id, the Undo, the error and the
// announcements. Features (tasks, cycles, periods, the editor) keep their own
// substate under model.ui and use this store to act.
//
// The store touches no DOM, no timer and no global: the daemon API, the clock
// and the id generator are injected, so every rule here runs under node.

import { ApiError } from "./api.mjs";
import { overtimeNow, parseRoute } from "./select.mjs";

/** How long an Undo stays, in milliseconds. */
export const UNDO_MS = 10000;

/**
 * @param {object} deps
 * @param {ReturnType<import("./api.mjs").createApi>} deps.api
 * @param {() => number} deps.now the clock, in milliseconds
 * @param {() => string} deps.newId a fresh idempotency id (a ULID)
 * @param {string} [deps.browserZone]
 * @param {string} [deps.hash] the location hash at start
 */
export function createStore({ api, now, newId, browserZone = "", hash = "" }) {
  const model = {
    now: now(),
    browserZone,
    route: parseRoute(hash),
    conn: "connecting", // connecting | live | lost
    loaded: false,
    loadError: null, // the ApiError of the last failed read
    state: null,
    receivedAt: 0,
    config: null,
    configGeneration: null,
    busy: 0,
    notice: null, // {seq, text}: a quiet sentence, never an error
    error: null, // see errorRecord
    undo: null, // {seq, label, ref, expiresAt}
    // What assistive technology is told: each message is appended, so two things said in
    // one moment are both heard; the last few stay so a message is read once and not again.
    announce: { polite: { seq: 0, items: [] }, assertive: { seq: 0, items: [] } },
    overtimeAnnounced: {},
    ui: {},
  };

  const listeners = new Set();
  const routeHooks = [];
  let seq = 0;
  let notifyQueued = false;

  function notify() {
    if (notifyQueued) return;
    notifyQueued = true;
    queueMicrotask(() => {
      notifyQueued = false;
      for (const l of listeners) l(model);
    });
  }

  function update(fn) {
    fn(model);
    notify();
  }

  function subscribe(fn) {
    listeners.add(fn);
    return () => listeners.delete(fn);
  }

  /** Says something to assistive technology, once. */
  function announce(text, { assertive = false } = {}) {
    const slot = assertive ? model.announce.assertive : model.announce.polite;
    slot.items.push({ seq: ++slot.seq, text });
    if (slot.items.length > 4) slot.items.shift();
    notify();
  }

  // ---- reading the state ----

  let refreshing = false;
  let again = false;

  /** Reads the state (and the configuration when its generation changed). Overlapping calls coalesce. */
  async function refresh() {
    if (refreshing) {
      again = true;
      return;
    }
    refreshing = true;
    try {
      do {
        again = false;
        const st = await api.getState();
        if (model.config === null || model.configGeneration !== st.version.config_generation) {
          model.config = await api.getConfig();
          model.configGeneration = st.version.config_generation;
        }
        applyState(st);
        model.loadError = null;
        model.loaded = true;
      } while (again);
    } catch (e) {
      model.loadError = e instanceof ApiError ? e : new ApiError({ kind: "network", detail: "The service could not be reached." });
    } finally {
      refreshing = false;
      notify();
    }
  }

  function applyState(st) {
    const prev = model.state;
    model.state = st;
    model.now = now();
    model.receivedAt = model.now;

    const wasRO = prev?.store?.state === "read_only";
    const isRO = st.store.state === "read_only";
    // Read-only mode begins with the banner, which is an alert and so announces itself.
    if (!isRO && wasRO) announce("The store can be written again.");

    for (const p of st.periods) {
      const was = prev?.periods?.find((x) => x.kind === p.kind);
      if (p.ended && !was?.ended) announce(p.banner);
    }
    checkOvertime();
  }

  /** Says once that a cycle has run out of time; a boost that ends overtime lets it say so again. */
  function checkOvertime() {
    const f = model.state?.focus;
    if (!f) {
      model.overtimeAnnounced = {};
      return;
    }
    const over = overtimeNow(model, f);
    if (over && !model.overtimeAnnounced[f.id]) {
      model.overtimeAnnounced[f.id] = true;
      announce(`${f.title} is out of time and is now in overtime.`);
    } else if (!over) {
      delete model.overtimeAnnounced[f.id];
    }
  }

  /** Moves the page's clock; the timers, the Undo's expiry and the overtime announcement follow it. */
  function tick(ms = now()) {
    model.now = ms;
    if (model.undo && ms >= model.undo.expiresAt) model.undo = null;
    checkOvertime();
    notify();
  }

  // ---- making a request ----

  /**
   * The record the page keeps of a failed request, for the error box: the
   * daemon's own sentence, its code and trace id, whether the request may be
   * repeated as it was, and the cycles it listed when it could not tell which
   * one was meant.
   */
  function errorRecord(action, id, err) {
    const e = err instanceof ApiError ? err : new ApiError({ kind: "protocol", detail: "The request failed in the page." });
    return {
      seq: ++seq,
      action,
      id,
      kind: e.kind,
      status: e.status,
      reason: e.reason,
      detail: e.detail,
      traceId: e.traceId,
      details: e.details,
      retryable: e.outcomeUnknown,
      readOnly: e.readOnly,
      // A cycle verb that several cycles could mean is a choice, never a guess.
      candidates: e.reason === "cycle_ambiguous" ? (e.details?.cycles ?? []) : [],
    };
  }

  /**
   * Sends a mutation with an idempotency id and handles its outcome.
   *
   * @param {{path: string, body: object, label: string, undo?: {label: string}}} action
   * @param {object} [opts]
   * @param {string} [opts.retryId] the id of a request whose outcome is unknown, to repeat it
   * @param {(model: object, rec: object) => void} [opts.errorTo] where a failure is recorded, when it is not the page's error box (a dialog, a form)
   * @returns {Promise<{ok: true, result: object} | {ok: false, error: ApiError, record: object}>}
   */
  async function perform(action, { retryId, errorTo } = {}) {
    const id = retryId ?? newId();
    model.busy++;
    if (!errorTo) model.error = null;
    notify();
    try {
      const result = await api.post(action.path, { ...action.body, id });
      model.busy--;
      if (result.changed === false) {
        // A request for a state that already holds is a success, not an error.
        model.notice = { seq: ++seq, text: result.note ?? "Nothing changed." };
      } else {
        model.notice = null;
        if (action.undo) setUndo(action.undo.label, result);
      }
      await refresh();
      return { ok: true, result };
    } catch (err) {
      model.busy--;
      const record = errorRecord(action, id, err);
      if (errorTo) errorTo(model, record);
      else model.error = record;
      if (record.readOnly) await refresh();
      notify();
      return { ok: false, error: err, record };
    }
  }

  /**
   * Asks the daemon what a request would do, without appending anything. A
   * preview is not an action: it has no Undo, changes no state and its failure
   * is the caller's to show.
   */
  async function preview(action) {
    try {
      return { ok: true, result: await api.post(action.path, { ...action.body, dry_run: true }) };
    } catch (err) {
      return { ok: false, error: err, record: errorRecord(action, "", err) };
    }
  }

  function setUndo(label, result) {
    const ref = result.batch_id ? { batch: result.batch_id } : { event: result.event_ids[0] };
    model.undo = { seq: ++seq, label, ref, expiresAt: now() + UNDO_MS };
    model.now = now();
  }

  /** Retracts what the Undo names. A refusal is shown as any other. */
  async function undo() {
    const u = model.undo;
    if (!u) return { ok: false };
    model.undo = null;
    const path = u.ref.batch ? `/api/v1/batches/${encodeURIComponent(u.ref.batch)}/retract` : `/api/v1/events/${encodeURIComponent(u.ref.event)}/retract`;
    const r = await perform({ path, body: {}, label: `Undo: ${u.label}` });
    if (r.ok) {
      model.notice = { seq: ++seq, text: `Undone: ${u.label}.` };
      notify();
    }
    return r;
  }

  /** Repeats the request whose outcome is unknown, with the same id. */
  async function retry() {
    const e = model.error;
    if (!e || !e.retryable) return { ok: false };
    return perform(e.action, { retryId: e.id });
  }

  /** Repeats a request after the operator chose which cycle was meant. */
  async function chooseCycle(cycleId) {
    const e = model.error;
    if (!e || e.candidates.length === 0) return { ok: false };
    const action = { ...e.action, body: { ...e.action.body, cycle_id: cycleId } };
    return perform(action);
  }

  function dismissError() {
    model.error = null;
    notify();
  }

  function dismissNotice() {
    model.notice = null;
    notify();
  }

  // ---- routes ----

  function setRoute(route) {
    model.route = route;
    for (const h of routeHooks) h(route);
    notify();
  }

  function onRoute(fn) {
    routeHooks.push(fn);
  }

  function setConn(c) {
    if (model.conn === c) return;
    const was = model.conn;
    model.conn = c;
    if (c === "lost") announce("The connection to the service was lost. The page shows the last state it had.");
    if (c === "live" && was === "lost") announce("The connection to the service is back.");
    notify();
  }

  return { model, update, subscribe, announce, refresh, perform, preview, undo, retry, chooseCycle, dismissError, dismissNotice, setRoute, onRoute, setConn, tick, api, newId, now };
}
