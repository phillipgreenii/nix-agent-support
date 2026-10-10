// Completing and skipping tasks. Completing is one action that means "now"; a
// time is sent only when the operator chooses one, so the page's clock never
// decides when something happened. Skipping is one action plus a reason that is
// not blank. The reasons used recently are read back from the corrected log, so
// nothing is kept in the browser.

import { completeTask, skipTask } from "./actions.mjs";
import { isBlank } from "./format.mjs";
import { displayZone, taskOfState } from "./select.mjs";
import { inputValueSecondsInZone, instantFromInput } from "./zone.mjs";

const RECENT_REASONS = 8;

/** The open form of a task, or null: {taskId, mode: "doneAt" | "skip", at, reason, problem}. */
export function taskForm(model) {
  return model.ui.taskForm ?? null;
}

/** Recent distinct skip reasons, newest first, from the events the editor lists. */
export function reasonsFrom(items) {
  const seen = new Set();
  const out = [];
  for (let i = items.length - 1; i >= 0 && out.length < RECENT_REASONS; i--) {
    const it = items[i];
    if (it.retracted || it.event.type !== "task.skipped") continue;
    const r = it.event.data.reason?.trim();
    if (r && !seen.has(r)) {
      seen.add(r);
      out.push(r);
    }
  }
  return out;
}

export function registerTasks(store) {
  const { model } = store;

  async function loadReasons() {
    try {
      const res = await store.api.getEvents({ view: "corrected", types: ["task.skipped"] });
      store.update((m) => {
        m.ui.reasons = reasonsFrom(res.events);
      });
    } catch {
      // The suggestions are a convenience; failing to read them is not worth an error.
    }
  }

  function currentTask() {
    const f = taskForm(model);
    return f ? taskOfState(model.state, f.taskId) : undefined;
  }

  return {
    init: loadReasons,

    "task.done": (task) => store.perform(completeTask(task)),

    "task.openDoneAt": (task) =>
      store.update((m) => {
        m.ui.taskForm = { taskId: task.id, mode: "doneAt", at: inputValueSecondsInZone(m.now, displayZone(m)), reason: "", problem: "" };
      }),

    "task.openSkip": (task) =>
      store.update((m) => {
        m.ui.taskForm = { taskId: task.id, mode: "skip", at: "", reason: "", problem: "" };
      }),

    "task.field": (name, value) =>
      store.update((m) => {
        if (m.ui.taskForm) {
          m.ui.taskForm[name] = value;
          m.ui.taskForm.problem = "";
        }
      }),

    "task.cancel": () =>
      store.update((m) => {
        m.ui.taskForm = null;
      }),

    "task.submit": async () => {
      const f = taskForm(model);
      const task = currentTask();
      if (!f || !task) return;
      let action;
      if (f.mode === "doneAt") {
        const ms = instantFromInput(f.at, displayZone(model));
        if (ms === null) {
          store.update((m) => {
            m.ui.taskForm.problem = "Enter the date and time the task was done, for example 2026-10-07T09:30.";
          });
          return;
        }
        action = completeTask(task, ms);
      } else {
        if (isBlank(f.reason)) {
          store.update((m) => {
            m.ui.taskForm.problem = "A skip needs a reason that is not blank.";
          });
          return;
        }
        action = skipTask(task, f.reason);
      }
      const r = await store.perform(action);
      if (r.ok) {
        store.update((m) => {
          m.ui.taskForm = null;
        });
        if (f.mode === "skip") loadReasons();
      }
    },
  };
}
