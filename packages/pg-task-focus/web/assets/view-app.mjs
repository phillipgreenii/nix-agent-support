// The whole page as a tree: the banners (the READ-ONLY banner first), the
// header and the navigation, the cycle panel beside the area (so the timer and
// its controls are visible in every area), the dialogs, the Undo toast and the
// live regions.

import { UNDO_MS } from "./store.mjs";
import { h } from "./vdom.mjs";
import { button } from "./view-common.mjs";
import { cyclePanel } from "./view-cycles.mjs";
import { entityDetail } from "./view-detail.mjs";
import { editorArea } from "./view-editor.mjs";
import { dialogs } from "./view-modals.mjs";
import { checklistArea } from "./view-tasks.mjs";
import { banners, header, nav } from "./view-top.mjs";

/**
 * @param {object} model
 * @param {(name: string, ...args: unknown[]) => unknown} dispatch
 * @returns the children of the page's root
 */
export function viewApp(model, dispatch) {
  const editor = model.route.name === "events";
  return [
    button(model, { mutates: false, class: "skip", onclick: () => dispatch("focus.main") }, "Skip to the content"),
    banners(model, dispatch),
    header(model, dispatch),
    nav(model),
    h(
      "div",
      { class: "layout" },
      cyclePanel(model, dispatch, { compact: editor }),
      h("main", { id: "main", tabindex: "-1", "aria-label": editor ? "Editor" : "Today" }, editor ? editorArea(model, dispatch) : [entityDetail(model), checklistArea(model, dispatch)]),
    ),
    model.state ? dialogs(model, dispatch) : null,
    undoToast(model, dispatch),
    liveRegions(model),
  ];
}

function undoToast(model, dispatch) {
  const u = model.undo;
  const showing = u && model.now < u.expiresAt;
  return h(
    "div",
    { class: showing ? "toast" : "toast toast-empty", role: "status", id: "undo-toast" },
    showing
      ? [
          h("span", {}, `${u.label}. `),
          button(model, { mutates: true, class: "primary", key: u.seq, "data-undo": "true", onclick: () => dispatch("undo") }, "Undo"),
          h("span", { class: "sub" }, ` Available for ${UNDO_MS / 1000} seconds. Undo is refused once later events depend on it.`),
        ]
      : null,
  );
}

function liveRegions(model) {
  const { polite, assertive } = model.announce;
  return h(
    "div",
    { class: "sr-only", id: "live" },
    h("div", { role: "status", "aria-live": "polite", "aria-relevant": "additions" }, polite.items.map((i) => h("p", { key: i.seq }, i.text))),
    h("div", { role: "alert", "aria-live": "assertive", "aria-relevant": "additions" }, assertive.items.map((i) => h("p", { key: i.seq }, i.text))),
  );
}
