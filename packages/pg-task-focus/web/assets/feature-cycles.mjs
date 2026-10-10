// Starting, pausing, resuming, switching, boosting, stopping and annotating
// cycles. Every cycle action names its cycle, so the daemon is never asked to
// guess; if it answers that several cycles could be meant anyway, the page
// shows the choice (the store's chooseCycle).

import { annotateCycle, boostCycle, pauseCycle, resumeCycle, startCycle, stopCycle, switchTo } from "./actions.mjs";
import { cycleTypeById, cycleTypes } from "./select.mjs";

/** The start form: the chosen type (empty is the first listed), the minutes override, a problem sentence. */
export function startForm(model) {
  return model.ui.cycleStart ?? { type: "", minutes: "", problem: "" };
}

/** The type the start form will start: the chosen one, else the first the picker lists. */
export function chosenType(model) {
  const f = startForm(model);
  const types = cycleTypes(model);
  return types.find((t) => t.id === f.type)?.id ?? types[0]?.id ?? "";
}

/**
 * A cycle's note form as the page shows it: the operator's draft once they
 * have typed, else the daemon's note and pairs, with a blank row for each key
 * of the type that has no pair yet.
 * @returns {{note: string, kv: {key: string, value: string}[], dirty: boolean}}
 */
export function draftOf(model, cycle) {
  const d = model.ui.drafts?.[cycle.id];
  if (d?.dirty) return d;
  const kv = (cycle.kv ?? []).map((p) => ({ key: p.key, value: p.value }));
  for (const key of cycleTypeById(model, cycle.type)?.keys ?? []) {
    if (!kv.some((p) => p.key === key)) kv.push({ key, value: "" });
  }
  return { note: cycle.note ?? "", kv, dirty: false };
}

/** The custom boost box of a cycle. */
export function boostBox(model, cycleId) {
  return model.ui.boost?.[cycleId] ?? "";
}

export function registerCycles(store) {
  const { model } = store;

  function edit(cycle, fn) {
    store.update((m) => {
      m.ui.drafts ??= {};
      const d = draftOf(m, cycle);
      const next = { note: d.note, kv: d.kv.map((p) => ({ ...p })), dirty: true };
      fn(next);
      m.ui.drafts[cycle.id] = next;
    });
  }

  return {
    "cycleStart.field": (name, value) =>
      store.update((m) => {
        m.ui.cycleStart = { ...startForm(m), [name]: value, problem: "" };
      }),

    "cycleStart.submit": async () => {
      const f = startForm(model);
      let minutes;
      if (f.minutes.trim() !== "") {
        minutes = Number(f.minutes);
        if (!Number.isInteger(minutes) || minutes < 1) {
          store.update((m) => {
            m.ui.cycleStart = { ...startForm(m), problem: "The minutes must be a whole number of at least 1." };
          });
          return;
        }
      }
      const type = chosenType(model);
      if (!type) return;
      const r = await store.perform(startCycle(type, minutes));
      if (r.ok) {
        store.update((m) => {
          m.ui.cycleStart = { type: f.type, minutes: "", problem: "" };
        });
      }
    },

    "cycle.pause": (c) => store.perform(pauseCycle(c)),
    "cycle.resume": (c) => store.perform(resumeCycle(c)),
    "cycle.stop": (c) => store.perform(stopCycle(c)),
    "cycle.switch": (c) => store.perform(switchTo(c)),
    "cycle.boost": (c, minutes) => store.perform(boostCycle(c, minutes)),

    "boost.field": (cycleId, value) =>
      store.update((m) => {
        m.ui.boost ??= {};
        m.ui.boost[cycleId] = value;
      }),

    "boost.custom": async (c) => {
      const n = Number(boostBox(model, c.id));
      if (!Number.isInteger(n) || n < 1) {
        store.announce("Enter a whole number of minutes, at least 1.", { assertive: true });
        return;
      }
      const r = await store.perform(boostCycle(c, n));
      if (r.ok) {
        store.update((m) => {
          m.ui.boost[c.id] = "";
        });
      }
    },

    "note.field": (c, value) => edit(c, (d) => (d.note = value)),
    "note.kv": (c, i, field, value) => edit(c, (d) => (d.kv[i][field] = value)),
    "note.add": (c) => edit(c, (d) => d.kv.push({ key: "", value: "" })),
    "note.remove": (c, i) => edit(c, (d) => d.kv.splice(i, 1)),

    "note.save": async (c) => {
      const d = draftOf(model, c);
      const r = await store.perform(annotateCycle(c, d.note, d.kv));
      if (r.ok) {
        store.update((m) => {
          if (m.ui.drafts) delete m.ui.drafts[c.id];
        });
      }
    },
  };
}
