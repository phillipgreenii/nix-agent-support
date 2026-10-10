// The editor: the events as a table in the corrected view with the original
// one action away, corrections, retractions, and the two named operations
// "Insert break (from, to)" and "End at (time)" with a before-and-after
// timeline. Nothing is ever deleted: a retraction is an event too, and can
// itself be retracted.

import { breakCycle, correctEvent, endCycleAt, retract } from "./actions.mjs";
import { batchOf, editableFields, effectiveMs, isCorrectable, lookupOf, sentence } from "./events.mjs";
import { isBlank } from "./format.mjs";
import { displayZone } from "./select.mjs";
import { cycleSegments, previewChange } from "./timeline.mjs";
import { addDays, dateInZone, inputValueSecondsInZone, instantFromInput, parseInstant, toRFC3339 } from "./zone.mjs";

export const CYCLE_EVENT_TYPES = ["cycle.started", "cycle.paused", "cycle.resumed", "cycle.stopped"];

/** The editor's state; created on first use. */
export function editorState(model) {
  return model.ui.editor ?? null;
}

function freshEditor(model) {
  const zone = displayZone(model);
  return {
    view: "corrected",
    from: addDays(dateInZone(model.now, zone), -2),
    to: "",
    type: "",
    items: null,
    loadedKey: "",
    loading: false,
    error: null,
    showOriginal: {},
    correct: null,
    rowError: null,
    op: null,
    cycleItems: null,
    missing: "",
  };
}

/**
 * The request the editor's filters make: the events from the first day to the
 * end of the last, read in the display zone.
 */
export function eventsQuery(ed, zone) {
  const q = { view: ed.view };
  if (ed.from) {
    const ms = instantFromInput(`${ed.from}T00:00`, zone);
    if (ms !== null) q.from = toRFC3339(ms);
  }
  if (ed.to) {
    const ms = instantFromInput(`${addDays(ed.to, 1)}T00:00`, zone);
    if (ms !== null) q.to = toRFC3339(ms);
  }
  if (ed.type) q.types = [ed.type];
  return q;
}

/** The cycles the events show, newest first, for the operations' picker. */
export function cyclesOf(items) {
  const out = [];
  for (const it of items ?? []) {
    if (it.retracted || it.event.type !== "cycle.started") continue;
    out.push({ id: it.event.data.cycle_id, title: it.event.data.title, startedAt: effectiveMs(it) });
  }
  return out.sort((a, b) => b.startedAt - a.startedAt);
}

/**
 * The fields a correction form starts from, as the strings its inputs show.
 * @returns {Record<string, unknown>}
 */
export function correctionInitial(item, zone) {
  const d = item.event.data;
  const values = { effective_at: inputValueSecondsInZone(effectiveMs(item), zone) };
  for (const f of editableFields(item.event.type, d)) {
    switch (f.kind) {
      case "instant":
        values[f.name] = d[f.name] ? inputValueSecondsInZone(parseInstant(d[f.name]), zone) : "";
        break;
      case "kv":
        values[f.name] = (d.kv ?? []).map((p) => ({ key: p.key, value: p.value }));
        break;
      case "number":
        values[f.name] = d[f.name] === undefined ? "" : String(d[f.name]);
        break;
      default:
        values[f.name] = d[f.name] ?? "";
    }
  }
  return values;
}

/**
 * The `fields` of a correction request: only what the operator changed.
 * @returns {{fields?: object, problem?: string}}
 */
export function correctionFields(item, values, zone) {
  const initial = correctionInitial(item, zone);
  const fields = {};
  const spec = [{ name: "effective_at", kind: "instant", label: "Time" }, ...editableFields(item.event.type, item.event.data)];
  for (const f of spec) {
    const now = values[f.name];
    const was = initial[f.name];
    if (JSON.stringify(now) === JSON.stringify(was)) continue;
    switch (f.kind) {
      case "instant": {
        const ms = instantFromInput(now, zone);
        if (ms === null) return { problem: `${f.label} is not a date and time.` };
        fields[f.name] = toRFC3339(ms);
        break;
      }
      case "number": {
        const n = Number(now);
        if (!Number.isInteger(n) || n < 1) return { problem: `${f.label} must be a whole number of at least 1.` };
        fields[f.name] = n;
        break;
      }
      case "kv":
        fields[f.name] = now.filter((p) => !isBlank(p.key) && !isBlank(p.value)).map((p) => ({ key: p.key.trim(), value: p.value }));
        break;
      default:
        fields[f.name] = now;
    }
  }
  if (Object.keys(fields).length === 0) return { problem: "Nothing was changed." };
  return { fields };
}

/**
 * The before-and-after of the operation form, or the sentence saying what is missing.
 * @returns {{preview?: ReturnType<typeof previewChange>, rec?: ReturnType<typeof cycleSegments>, change?: object, problem?: string}}
 */
export function operationPreview(model) {
  const ed = editorState(model);
  const op = ed?.op;
  if (!op || !ed.cycleItems) return { problem: "Reading the cycles…" };
  if (!op.cycleId) return { problem: "Choose the cycle." };
  const zone = displayZone(model);
  const rec = cycleSegments(ed.cycleItems, op.cycleId);
  if (!rec.found) return { problem: "The log has no start for that cycle." };
  if (op.kind === "break") {
    const from = instantFromInput(op.from, zone);
    const to = instantFromInput(op.to, zone);
    if (from === null || to === null) return { rec, problem: "Enter when the break started and when it ended." };
    if (to <= from) return { rec, problem: "The break must start before it ends." };
    const change = { kind: "break", from, to };
    return { rec, change, preview: previewChange(rec, change, model.now) };
  }
  const at = instantFromInput(op.at, zone);
  if (at === null) return { rec, problem: "Enter the time the cycle really ended." };
  const change = { kind: "endat", at };
  return { rec, change, preview: previewChange(rec, change, model.now) };
}

export function registerEditor(store) {
  const { model } = store;

  function ed(m) {
    m.ui.editor ??= freshEditor(m);
    return m.ui.editor;
  }

  async function load() {
    const e = ed(model);
    const zone = displayZone(model);
    const q = eventsQuery(e, zone);
    const key = JSON.stringify([q, model.state?.version]);
    store.update((m) => {
      const x = ed(m);
      x.loading = true;
      x.loadedKey = key;
    });
    try {
      const res = await store.api.getEvents(q);
      store.update((m) => {
        const x = ed(m);
        x.items = res.events;
        x.loading = false;
        x.error = null;
      });
    } catch (err) {
      store.update((m) => {
        const x = ed(m);
        x.loading = false;
        x.error = err;
      });
    }
    ensureRouteEvent();
  }

  // An event named by the route that the filters hide widens the range once, to all of the log.
  function ensureRouteEvent() {
    const route = model.route;
    const e = ed(model);
    if (route.name !== "events" || !route.id || e.items === null || e.loading) return;
    if (e.items.some((it) => it.id === route.id)) {
      store.update((m) => {
        ed(m).missing = "";
        m.focusRequest = { seq: (m.focusRequest?.seq ?? 0) + 1, kind: "event", id: route.id };
      });
    } else if (e.from || e.to || e.type) {
      store.update((m) => {
        const x = ed(m);
        x.from = "";
        x.to = "";
        x.type = "";
      });
      load();
    } else {
      store.update((m) => {
        ed(m).missing = route.id;
      });
    }
  }

  // The editor follows the log: while it is open it reads again whenever the state's version changed.
  store.subscribe((m) => {
    if (m.route.name !== "events" || !m.state) return;
    const e = ed(m);
    const key = JSON.stringify([eventsQuery(e, displayZone(m)), m.state.version]);
    if (!e.loading && e.loadedKey !== key) load();
  });
  store.onRoute((route) => {
    if (route.name === "events") {
      store.update((m) => {
        ed(m);
      });
      if (model.state && !ed(model).loading) ensureRouteEvent();
    }
  });

  const setErr = (field) => (m, rec) => {
    if (field === "correct") ed(m).correct.error = rec;
    else if (field === "op") ed(m).op.error = rec;
    else ed(m).rowError = { id: m.ui.editor.rowErrorFor, record: rec };
  };

  async function loadCycleItems() {
    try {
      const res = await store.api.getEvents({ view: "corrected", types: CYCLE_EVENT_TYPES });
      store.update((m) => {
        ed(m).cycleItems = res.events;
      });
    } catch (err) {
      store.update((m) => {
        ed(m).op.error = { detail: err.detail ?? "The cycles could not be read.", reason: err.reason ?? "", traceId: err.traceId ?? "", retryable: false, candidates: [], details: null };
      });
    }
  }

  return {
    "editor.setView": (view) => {
      store.update((m) => {
        ed(m).view = view;
      });
    },
    "editor.filter": (name, value) =>
      store.update((m) => {
        ed(m)[name] = value;
      }),
    "editor.showAll": () =>
      store.update((m) => {
        const x = ed(m);
        x.from = "";
        x.to = "";
        x.type = "";
      }),
    "editor.reload": load,

    "editor.toggleOriginal": (id) =>
      store.update((m) => {
        const x = ed(m);
        x.showOriginal[id] = !x.showOriginal[id];
      }),

    "editor.openCorrect": (item) =>
      store.update((m) => {
        const x = ed(m);
        if (!isCorrectable(item.event.type)) return;
        x.correct = { id: item.id, values: correctionInitial(item, displayZone(m)), reason: "", problem: "", error: null };
      }),
    "editor.correctField": (name, value) =>
      store.update((m) => {
        const c = ed(m).correct;
        if (c) {
          c.values[name] = value;
          c.problem = "";
        }
      }),
    "editor.correctKv": (name, i, field, value) =>
      store.update((m) => {
        ed(m).correct.values[name][i][field] = value;
      }),
    "editor.correctKvAdd": (name) =>
      store.update((m) => {
        ed(m).correct.values[name].push({ key: "", value: "" });
      }),
    "editor.correctKvRemove": (name, i) =>
      store.update((m) => {
        ed(m).correct.values[name].splice(i, 1);
      }),
    "editor.correctReason": (value) =>
      store.update((m) => {
        ed(m).correct.reason = value;
      }),
    "editor.cancelCorrect": () =>
      store.update((m) => {
        ed(m).correct = null;
      }),
    "editor.submitCorrect": async () => {
      const c = ed(model).correct;
      const item = ed(model).items?.find((it) => it.id === c?.id);
      if (!c || !item) return;
      const { fields, problem } = correctionFields(item, c.values, displayZone(model));
      if (problem) {
        store.update((m) => {
          ed(m).correct.problem = problem;
        });
        return;
      }
      store.update((m) => {
        ed(m).correct.error = null;
      });
      const r = await store.perform(correctEvent(item.id, fields, c.reason), { errorTo: setErr("correct") });
      if (r.ok) {
        store.update((m) => {
          ed(m).correct = null;
        });
      }
    },

    "editor.retract": async (item) => {
      const e = ed(model);
      const look = lookupOf(e.items ?? [], model.state);
      const batch = batchOf(item);
      store.update((m) => {
        ed(m).rowError = null;
        m.ui.editor.rowErrorFor = item.id;
      });
      await store.perform(retract(item, batch, "", sentence(item, look, displayZone(model))), { errorTo: setErr("row") });
    },
    "editor.dismissRowError": () =>
      store.update((m) => {
        ed(m).rowError = null;
      }),

    "op.open": async (kind) => {
      store.update((m) => {
        const x = ed(m);
        x.op = { kind, cycleId: m.state?.focus?.id ?? "", from: "", to: "", at: "", problem: "", error: null };
        x.cycleItems = null;
      });
      await loadCycleItems();
      store.update((m) => {
        const x = ed(m);
        if (x.op && !x.op.cycleId) x.op.cycleId = cyclesOf(x.cycleItems)[0]?.id ?? "";
      });
    },
    "op.field": (name, value) =>
      store.update((m) => {
        const o = ed(m).op;
        if (o) {
          o[name] = value;
          o.problem = "";
        }
      }),
    "op.cancel": () =>
      store.update((m) => {
        ed(m).op = null;
      }),
    "op.submit": async () => {
      const { change, rec, problem } = operationPreview(model);
      const e = ed(model);
      if (problem || !change) {
        store.update((m) => {
          ed(m).op.problem = problem ?? "Complete the form.";
        });
        return;
      }
      store.update((m) => {
        ed(m).op.error = null;
      });
      const action = change.kind === "break" ? breakCycle(e.op.cycleId, rec.title, change.from, change.to) : endCycleAt(e.op.cycleId, rec.title, change.at);
      const r = await store.perform(action, { errorTo: setErr("op") });
      if (r.ok) {
        store.update((m) => {
          ed(m).op = null;
        });
      }
    },
  };
}
