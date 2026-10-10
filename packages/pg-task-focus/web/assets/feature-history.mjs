// Deep links: #/tasks/<id> and #/cycles/<id>. A task or cycle of the current
// state is shown where the page already shows it, marked and focused. One that
// is not (a task of an earlier period, a stopped cycle) is shown from the log,
// so a link in a notification never lands on nothing. An id the log does not
// know is said in words.

import { cycleOfState, taskOfState } from "./select.mjs";

const TASK_TYPES = ["task.materialized", "task.completed", "task.skipped", "task.missed", "task.withdrawn", "task.reinstated"];
const CYCLE_TYPES = ["cycle.started", "cycle.paused", "cycle.resumed", "cycle.boosted", "cycle.stopped", "cycle.annotated"];

/** The history the page loaded for the route, or null: {key, items, loading, error}. */
export function historyOf(model) {
  return model.ui.history ?? null;
}

/** The key naming the entity of a route, or "" for a route that names none. */
export function routeKey(route) {
  return route.name === "task" || route.name === "cycle" ? `${route.name}:${route.id}` : "";
}

/** The events of a task or cycle id, from a list of event items. */
export function entityItems(items, route) {
  return items.filter((it) => {
    const d = it.event.data ?? {};
    if (route.name === "task") return d.task_id === route.id;
    return d.cycle_id === route.id || (it.event.type === "cycle.started" && d.interrupts === route.id);
  });
}

export function registerHistory(store) {
  const { model } = store;

  async function load(route) {
    const key = routeKey(route);
    const version = JSON.stringify(model.state?.version);
    store.update((m) => {
      m.ui.history = { key, version, items: null, loading: true, error: null };
    });
    try {
      const res = await store.api.getEvents({ view: "corrected", types: route.name === "task" ? TASK_TYPES : CYCLE_TYPES });
      store.update((m) => {
        if (m.ui.history?.key === key) m.ui.history = { key, version, items: res.events, loading: false, error: null };
      });
    } catch (err) {
      store.update((m) => {
        if (m.ui.history?.key === key) m.ui.history = { key, version, items: null, loading: false, error: err };
      });
    }
  }

  // Follows the route and the state: once the state is known, an entity it holds is focused, any other is read from the log.
  store.subscribe((m) => {
    const key = routeKey(m.route);
    if (!key || !m.state) return;
    const inState = m.route.name === "task" ? !!taskOfState(m.state, m.route.id) : !!cycleOfState(m.state, m.route.id);
    if (inState) {
      if (m.ui.focusedKey !== key) {
        m.ui.focusedKey = key;
        m.focusRequest = { seq: (m.focusRequest?.seq ?? 0) + 1, kind: m.route.name, id: m.route.id };
      }
    } else {
      const h = historyOf(m);
      if (!h || h.key !== key || (!h.loading && h.version !== JSON.stringify(m.state.version))) load(m.route);
    }
  });
  store.onRoute((route) => {
    const key = routeKey(route);
    if (key !== model.ui.focusedKey) model.ui.focusedKey = "";
  });

  return {};
}
