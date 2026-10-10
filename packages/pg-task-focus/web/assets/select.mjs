// Selectors: what the page derives from the model, with no side effect. The
// model's `state` is the daemon's /state document, `config` its /config, and
// `receivedAt` is when this page received that state, on this page's clock.

import { shortTitle, timerText } from "./format.mjs";

export const KINDS = ["day", "week", "sprint"];

export const KIND_TITLES = { day: "Day", week: "Week", sprint: "Sprint" };

/** Whether the store is read-only: every control that mutates is then disabled. */
export function isReadOnly(model) {
  return model.state?.store?.state === "read_only";
}

/**
 * The sentence every client shows while the store is read-only, or null.
 * It is the daemon's own wording: `READ-ONLY: <reason>. Restart pg-task-focus to recover`.
 */
export function readOnlySentence(store) {
  if (!store || store.state !== "read_only") return null;
  return `READ-ONLY: ${store.reason}. Restart pg-task-focus to recover`;
}

/** The current period of a kind, or undefined. */
export function periodOf(state, kind) {
  return state?.periods?.find((p) => p.kind === kind);
}

/**
 * The zone the page shows times in: the active day period's, then the week's
 * and the sprint's, then the browser's. Every input and every time the page
 * shows names it.
 */
export function displayZone(model) {
  for (const k of KINDS) {
    const p = periodOf(model.state, k);
    if (p?.zone) return p.zone;
  }
  return model.browserZone || "UTC";
}

/** The periods that have ended and wait to be rolled over, in day, week, sprint order. */
export function endedPeriods(state) {
  return (state?.periods ?? []).filter((p) => p.ended);
}

/**
 * The tasks of each current period, grouped as the daemon ordered them.
 * @returns {{period: object, groups: {name: string, tasks: object[]}[]}[]}
 */
export function checklists(state) {
  const out = [];
  for (const kind of KINDS) {
    const period = periodOf(state, kind);
    if (!period) continue;
    const groups = [];
    for (const t of state.tasks.filter((x) => x.kind === kind)) {
      const name = t.group ?? "";
      let g = groups[groups.length - 1];
      if (!g || g.name !== name) {
        g = { name, tasks: [] };
        groups.push(g);
      }
      g.tasks.push(t);
    }
    out.push({ period, groups });
  }
  return out;
}

/** Whether a task can be completed or skipped from the page: open, or missed (a late resolution). */
export function isActionable(task) {
  return task.status === "open" || task.status === "missed";
}

/**
 * The remaining seconds of a cycle now. Paused and stopped cycles do not move.
 * The page counts from the daemon's read: the daemon's remaining seconds at
 * its read, less the time since this page received it on this page's own
 * clock, so a skewed browser clock cannot show a wrong time.
 */
export function remainingNow(model, cycle) {
  if (!cycle) return 0;
  if (cycle.status !== "running") return cycle.remaining_seconds;
  const since = Math.max(0, (model.now - model.receivedAt) / 1000);
  return cycle.remaining_seconds - since;
}

/** The text of a cycle's timer now. */
export function timerNow(model, cycle) {
  return timerText(remainingNow(model, cycle));
}

/** Whether the focus cycle is in overtime now. */
export function overtimeNow(model, cycle) {
  return !!cycle && cycle.status === "running" && remainingNow(model, cycle) < 0;
}

/** The text of the browser tab: the running timer or the overtime ("Deep +04:10"). */
export function tabTitle(model) {
  const prefix = isReadOnly(model) ? "READ-ONLY " : "";
  const f = model.state?.focus;
  if (f) return `${prefix}${shortTitle(f.title)} ${timerNow(model, f)}`;
  return `${prefix}pg-task-focus`;
}

/**
 * The configured cycle types for the type picker: those of the active profile,
 * in the profile's order, then the others, which are flagged as not in it.
 */
export function cycleTypes(model) {
  const cfg = model.config;
  if (!cfg) return [];
  const profile = cfg.profiles.find((p) => p.name === model.state?.profile);
  const listed = profile ? profile.cycles : [];
  const byId = new Map(cfg.cycles.map((c) => [c.id, c]));
  const out = [];
  for (const id of listed) {
    const c = byId.get(id);
    if (c) out.push({ ...c, inProfile: true });
  }
  for (const c of cfg.cycles) {
    if (!listed.includes(c.id)) out.push({ ...c, inProfile: false });
  }
  return out;
}

/** The configured type with an id, or undefined. */
export function cycleTypeById(model, id) {
  return model.config?.cycles.find((c) => c.id === id);
}

/** The cycles that are not stopped now: the focus and every dimmed cycle. */
export function activeCycles(state) {
  const out = [];
  if (state?.focus) out.push(state.focus);
  for (const d of state?.dimmed ?? []) out.push(d);
  return out;
}

/** Looks up a cycle of the current state (the focus or a dimmed one) by id. */
export function cycleOfState(state, id) {
  return activeCycles(state).find((c) => c.id === id);
}

/** Looks up a task of the current periods by id. */
export function taskOfState(state, id) {
  return state?.tasks?.find((t) => t.id === id);
}

/** The text of the header's next-task line. */
export function nextText(task, fmt) {
  if (!task) return "Nothing is due";
  return fmt(task);
}

// ---- routes: #/, #/tasks/<id>, #/cycles/<id>, #/events, #/events/<id> ----

/**
 * Reads a location hash into a route. An empty or unknown hash is the today
 * view; a malformed escape in an id is left as typed.
 * @returns {{name: "today"|"task"|"cycle"|"events", id?: string}}
 */
export function parseRoute(hash) {
  const h = (hash ?? "").replace(/^#/, "");
  const parts = h.split("/").filter((s) => s !== "");
  const decode = (s) => {
    try {
      return decodeURIComponent(s);
    } catch {
      return s;
    }
  };
  if (parts[0] === "tasks" && parts[1]) return { name: "task", id: decode(parts[1]) };
  if (parts[0] === "cycles" && parts[1]) return { name: "cycle", id: decode(parts[1]) };
  if (parts[0] === "events") return parts[1] ? { name: "events", id: decode(parts[1]) } : { name: "events" };
  return { name: "today" };
}

/** The hash that names a route. */
export function routeHash(route) {
  switch (route.name) {
    case "task":
      return `#/tasks/${encodeURIComponent(route.id)}`;
    case "cycle":
      return `#/cycles/${encodeURIComponent(route.id)}`;
    case "events":
      return route.id ? `#/events/${encodeURIComponent(route.id)}` : "#/events";
    default:
      return "#/";
  }
}
