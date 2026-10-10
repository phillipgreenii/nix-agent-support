// The controller: every thing the operator can do, by name. A view never
// calls the store directly; it calls dispatch("task.done", task), so a test can
// press a control and see which intent it raised, and the intents are all in
// one table.

import { registerCycles } from "./feature-cycles.mjs";
import { registerEditor } from "./feature-editor.mjs";
import { registerHistory } from "./feature-history.mjs";
import { registerPeriods } from "./feature-periods.mjs";
import { registerTasks } from "./feature-tasks.mjs";

/**
 * @param {ReturnType<import("./store.mjs").createStore>} store
 */
export function createController(store) {
  const features = [registerTasks(store), registerCycles(store), registerPeriods(store), registerEditor(store), registerHistory(store)];
  const handlers = Object.assign({}, ...features, {
    undo: () => store.undo(),
    "error.retry": () => store.retry(),
    "error.choose": (cycleId) => store.chooseCycle(cycleId),
    "error.dismiss": () => store.dismissError(),
    "notice.dismiss": () => store.dismissNotice(),
    refresh: () => store.refresh(),
    "focus.main": () =>
      store.update((m) => {
        m.focusRequest = { seq: (m.focusRequest?.seq ?? 0) + 1, kind: "main" };
      }),
  });

  function dispatch(name, ...args) {
    const h = handlers[name];
    if (typeof h !== "function") throw new Error(`unknown intent ${name}`);
    return h(...args);
  }

  /** Starts what needs the daemon: the first read, and the suggestions that come from the log. */
  async function init() {
    await store.refresh();
    for (const f of features) if (typeof f.init === "function") f.init();
  }

  return { dispatch, init, intents: Object.keys(handlers) };
}
