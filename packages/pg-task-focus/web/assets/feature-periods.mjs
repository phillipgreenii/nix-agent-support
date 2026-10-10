// The period modal and the profile modal. Both show the daemon's preview of a
// change before it can be confirmed, and carry the preview's version back so a
// preview the log has moved past is refused and shown again.
//
// A period change is refused while any cycle is running or paused: the modal
// then lists those cycles with Stop and "End at" and does not confirm. "End at"
// is never filled in: the operator says when the cycle really ended.

import { changePeriods, changeProfile, stopCycle } from "./actions.mjs";
import { isBlank } from "./format.mjs";
import { KINDS, activeCycles, displayZone, periodOf } from "./select.mjs";
import { addDays, dateInZone, daysBetween, instantFromInput, isZone, toRFC3339 } from "./zone.mjs";

const DATE = /^\d{4}-\d{2}-\d{2}$/;

// ---- pure helpers ----

/**
 * The period form as it opens. The zone defaults from the browser. Each kind
 * that has ended is selected (all three on an empty log); the start is today
 * in that zone and, for a week or sprint, the end is the start plus the length
 * of the period it follows (a week with no predecessor is seven days).
 */
export function defaultPeriodForm(model) {
  const state = model.state;
  const tz = model.browserZone || displayZone(model);
  const today = dateInZone(model.now, tz);
  const kinds = {};
  for (const kind of KINDS) {
    const cur = periodOf(state, kind);
    let end = "";
    if (kind !== "day") {
      if (cur) end = addDays(today, daysBetween(cur.start, cur.end));
      else if (kind === "week") end = addDays(today, 6);
    }
    kinds[kind] = { on: !state?.initialized || !!cur?.ended, start: today, end, label: "" };
  }
  return { tz, profile: "", backdate: "", kinds };
}

/** Problems with the form that the page can see without asking the daemon, as sentences. */
export function periodProblems(form) {
  const out = [];
  const on = KINDS.filter((k) => form.kinds[k].on);
  if (on.length === 0) out.push("Choose at least one period to begin.");
  if (isBlank(form.tz)) out.push("Name the zone the periods are in.");
  for (const k of on) {
    const f = form.kinds[k];
    if (!DATE.test(f.start)) out.push(`Give the ${k}'s first day as a date.`);
    if (k !== "day") {
      if (!DATE.test(f.end)) out.push(`Give the ${k}'s last day as a date.`);
      else if (DATE.test(f.start) && f.end < f.start) out.push(`The ${k} cannot end before it starts.`);
    }
  }
  if (!isBlank(form.backdate)) {
    if (!isBlank(form.tz) && !isZone(form.tz.trim())) out.push(`This browser cannot read a time in the zone ${form.tz.trim()}, so it cannot back-date in it.`);
    else if (instantFromInput(form.backdate, form.tz.trim()) === null) out.push("The back-dated time is not a date and time.");
  }
  return out;
}

/** The request body of the form, without its id, version or dry-run flag. */
export function periodRequest(form) {
  const tz = form.tz.trim();
  const changes = KINDS.filter((k) => form.kinds[k].on).map((k) => {
    const f = form.kinds[k];
    const c = { kind: k, start: f.start, tz };
    if (k !== "day") c.end = f.end;
    if (!isBlank(f.label)) c.label = f.label.trim();
    return c;
  });
  const req = { changes };
  if (!isBlank(form.backdate)) req.effective_at = toRFC3339(instantFromInput(form.backdate, tz));
  if (!isBlank(form.profile)) req.profile = form.profile;
  return req;
}

/** What identifies the request a preview answers: the preview is current only while the form still produces it. */
export function previewKey(form) {
  return JSON.stringify(periodRequest(form));
}

/**
 * The rollover part of the confirm request.
 * @param {{mode: "missed"|"skipAll", skipAllReason: string, perTask: Record<string, {skip: boolean, reason: string}>}} r
 * @param {{id: string}[]} leaving the open tasks of the periods being left
 */
export function rolloverBody(r, leaving) {
  const body = {};
  if (r.mode === "skipAll") body.skip_all_reason = r.skipAllReason.trim();
  const overrides = [];
  for (const t of leaving) {
    const p = r.perTask[t.id];
    if (p?.skip) overrides.push({ task_id: t.id, reason: p.reason.trim() });
  }
  if (overrides.length > 0) body.overrides = overrides;
  return body;
}

/** Problems with the rollover choices: a skip with no reason. */
export function rolloverProblems(r, leaving) {
  const out = [];
  if (r.mode === "skipAll" && isBlank(r.skipAllReason)) out.push("Give the one reason every open task is skipped for.");
  for (const t of leaving) {
    const p = r.perTask[t.id];
    if (p?.skip && isBlank(p.reason)) out.push(`Give a reason for skipping ${t.title}, or leave it to be marked missed.`);
  }
  return out;
}

function setPath(obj, path, value) {
  const keys = path.split(".");
  let o = obj;
  for (const k of keys.slice(0, -1)) o = o[k];
  o[keys[keys.length - 1]] = value;
}

// ---- the features ----

export function periodState(model) {
  return model.ui.period ?? null;
}

export function profileState(model) {
  return model.ui.profile ?? null;
}

/** Whether the period modal's preview still answers the form: the preview is current only while the form makes the request it was made for. */
export function previewIsCurrent(p) {
  return !!p?.preview && periodProblems(p.form).length === 0 && p.preview.key === previewKey(p.form);
}

/** The cycles that stop a period change now: the preview's list, else the cycles the state says are not stopped. */
export function blockingCycles(model) {
  const p = periodState(model);
  if (previewIsCurrent(p)) return p.preview.result.preview.blocking_cycles;
  return activeCycles(model.state).map((c) => ({ id: c.id, title: c.title, status: c.status, started_at: c.started_at }));
}

export function registerPeriods(store) {
  const { model } = store;

  const periodError = (m, rec) => {
    m.ui.period.error = rec;
  };
  const profileError = (m, rec) => {
    m.ui.profile.error = rec;
  };

  async function preview() {
    const p = periodState(model);
    if (!p) return;
    const problems = periodProblems(p.form);
    if (problems.length > 0) {
      store.update((m) => {
        m.ui.period.problems = problems;
      });
      return;
    }
    const key = previewKey(p.form);
    store.update((m) => {
      m.ui.period.problems = [];
      m.ui.period.error = null;
      m.ui.period.previewing = true;
    });
    const r = await store.preview(changePeriods(periodRequest(p.form)));
    store.update((m) => {
      const pp = m.ui.period;
      if (!pp) return;
      pp.previewing = false;
      if (r.ok) {
        pp.preview = { key, result: r.result };
        const leavingIds = new Set(r.result.preview.leaving.map((t) => t.id));
        for (const id of Object.keys(pp.rollover.perTask)) if (!leavingIds.has(id)) delete pp.rollover.perTask[id];
      } else {
        pp.preview = null;
        pp.error = r.record;
      }
    });
  }

  async function confirm() {
    const p = periodState(model);
    if (!previewIsCurrent(p)) return;
    const leaving = p.preview.result.preview.leaving;
    const problems = rolloverProblems(p.rollover, leaving);
    if (problems.length > 0) {
      store.update((m) => {
        m.ui.period.problems = problems;
      });
      return;
    }
    const req = { ...periodRequest(p.form), ...rolloverBody(p.rollover, leaving), expected_version: p.preview.result.state_version };
    const r = await store.perform(changePeriods(req), { errorTo: periodError });
    if (r.ok) {
      store.update((m) => {
        m.ui.period = null;
        m.ui.modal = null;
      });
    } else if (r.record.reason === "stale_preview") {
      // What the operator saw is not what would happen now: show it again.
      await preview();
      store.update((m) => {
        if (m.ui.period) m.ui.period.stale = true;
      });
    }
  }

  async function stopBlocker(cycle, atMs) {
    const action = { ...stopCycle(cycle, atMs), undo: undefined };
    const r = await store.perform(action, { errorTo: periodError });
    if (r.ok && periodState(model)?.preview) await preview();
    return r;
  }

  return {
    "period.open": () =>
      store.update((m) => {
        m.ui.period = {
          form: defaultPeriodForm(m),
          preview: null,
          previewing: false,
          problems: [],
          error: null,
          stale: false,
          rollover: { mode: "missed", skipAllReason: "", perTask: {} },
          endAt: {},
          endAtProblem: {},
        };
        m.ui.modal = "period";
      }),

    "period.close": () =>
      store.update((m) => {
        m.ui.period = null;
        if (m.ui.modal === "period") m.ui.modal = null;
      }),

    "period.field": (path, value) =>
      store.update((m) => {
        if (!m.ui.period) return;
        setPath(m.ui.period.form, path, value);
        m.ui.period.problems = [];
        m.ui.period.stale = false;
      }),

    "period.preview": preview,
    "period.confirm": confirm,

    "rollover.mode": (mode) =>
      store.update((m) => {
        m.ui.period.rollover.mode = mode;
      }),
    "rollover.reason": (value) =>
      store.update((m) => {
        m.ui.period.rollover.skipAllReason = value;
        m.ui.period.problems = [];
      }),
    "rollover.task": (taskId, skip, reason) =>
      store.update((m) => {
        const cur = m.ui.period.rollover.perTask[taskId] ?? { skip: false, reason: "" };
        m.ui.period.rollover.perTask[taskId] = { skip: skip ?? cur.skip, reason: reason ?? cur.reason };
        m.ui.period.problems = [];
      }),

    "blocker.stop": (cycle) => stopBlocker(cycle),
    "blocker.endAtField": (cycleId, value) =>
      store.update((m) => {
        m.ui.period.endAt[cycleId] = value;
        m.ui.period.endAtProblem[cycleId] = "";
      }),
    "blocker.endAt": async (cycle) => {
      const p = periodState(model);
      const ms = instantFromInput(p.endAt[cycle.id] ?? "", displayZone(model));
      if (ms === null) {
        store.update((m) => {
          m.ui.period.endAtProblem[cycle.id] = "Enter the date and time the cycle really ended.";
        });
        return;
      }
      await stopBlocker(cycle, ms);
    },

    "profile.open": () =>
      store.update((m) => {
        m.ui.profile = { selected: m.state?.profile ?? "", preview: null, previewing: false, error: null };
        m.ui.modal = "profile";
      }),
    "profile.close": () =>
      store.update((m) => {
        m.ui.profile = null;
        if (m.ui.modal === "profile") m.ui.modal = null;
      }),
    "profile.select": (name) =>
      store.update((m) => {
        m.ui.profile.selected = name;
        m.ui.profile.preview = null;
        m.ui.profile.error = null;
      }),
    "profile.preview": async () => {
      const p = profileState(model);
      if (!p || isBlank(p.selected)) return;
      const selected = p.selected;
      store.update((m) => {
        m.ui.profile.previewing = true;
        m.ui.profile.error = null;
      });
      const r = await store.preview(changeProfile(selected));
      store.update((m) => {
        const pp = m.ui.profile;
        if (!pp) return;
        pp.previewing = false;
        if (r.ok) pp.preview = { profile: selected, result: r.result };
        else pp.error = r.record;
      });
    },
    "profile.confirm": async () => {
      const p = profileState(model);
      if (!p?.preview || p.preview.profile !== p.selected) return;
      const r = await store.perform(changeProfile(p.selected, p.preview.result.state_version), { errorTo: profileError });
      if (r.ok) {
        store.update((m) => {
          m.ui.profile = null;
          m.ui.modal = null;
        });
      }
    },
  };
}
