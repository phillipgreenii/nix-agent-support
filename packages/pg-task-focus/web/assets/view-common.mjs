// The pieces every view is built from. Two rules live here so they cannot be
// forgotten at a call site:
//
//  - A control must say whether it changes something (`mutates`). A control that
//    does is disabled while the store is read-only, by this one function, and is
//    marked data-mutates so a test can walk the whole page and check it.
//  - An error shows the daemon's own sentence, unchanged.

import { h } from "./vdom.mjs";
import { isReadOnly } from "./select.mjs";

/**
 * A form control or button.
 * @param {object} model
 * @param {"button"|"input"|"select"|"textarea"} tag
 * @param {{mutates: boolean} & Record<string, unknown>} props
 */
export function control(model, tag, props, ...children) {
  const { mutates, ...rest } = props ?? {};
  if (typeof mutates !== "boolean") throw new Error(`a ${tag} must say whether it mutates`);
  const p = { ...rest };
  if (tag === "button" && p.type === undefined) p.type = "button";
  p.disabled = !!p.disabled || (mutates && isReadOnly(model));
  if (mutates) p["data-mutates"] = "true";
  return h(tag, p, ...children);
}

export const button = (model, props, ...children) => control(model, "button", props, ...children);

/**
 * A labelled field: a label tied to its control, and optional hint text.
 * @param {object} model
 * @param {{label: string, hint?: string, id: string, tag?: string, mutates: boolean} & Record<string, unknown>} spec
 */
export function field(model, { label, hint, id, tag = "input", ...props }, ...children) {
  if (!id) throw new Error("a field needs a stable id");
  const fid = id;
  const hid = hint ? `${fid}-hint` : undefined;
  return h(
    "div",
    { class: "field" },
    h("label", { for: fid }, label),
    control(model, tag, { ...props, id: fid, "aria-describedby": hid }, ...children),
    hint ? h("p", { class: "hint", id: hid }, hint) : null,
  );
}

/** A heading with a stable id for aria-labelledby. */
export function heading(level, id, text, props = {}) {
  return h(`h${level}`, { id, ...props }, text);
}

/** A link into the page's own routes. */
export function routeLink(href, text, props = {}) {
  return h("a", { href, ...props }, text);
}

/**
 * The error box of a failed request: the daemon's own sentence, its code and trace
 * id for a bug report, and the ways forward the failure allows.
 *
 * @param {object} model
 * @param {object} rec an error record (store.perform's) or {detail, reason, traceId, ...}
 * @param {{onRetry?: () => void, onChoose?: (cycleId: string) => void, onDismiss?: () => void}} [on]
 */
export function errorBox(model, rec, on = {}) {
  if (!rec) return null;
  const events = rec.details?.events ?? [];
  return h(
    "div",
    { class: "error", role: "alert", "data-reason": rec.reason || undefined },
    h("p", { class: "error-detail" }, rec.detail),
    rec.kind === "network" || rec.retryable
      ? h("p", { class: "error-note" }, "The outcome is unknown. Repeating the request sends the same request id, so it takes effect at most once.")
      : null,
    rec.candidates?.length > 0
      ? h(
          "div",
          { class: "choices" },
          h("p", {}, "Which cycle do you mean?"),
          h(
            "ul",
            {},
            rec.candidates.map((c) =>
              h("li", { key: c.id }, button(model, { mutates: true, onclick: () => on.onChoose?.(c.id) }, `Use ${c.title}`)),
            ),
          ),
        )
      : null,
    events.length > 0
      ? h(
          "p",
          { class: "error-events" },
          "Events named: ",
          events.map((id, i) => [i > 0 ? ", " : "", routeLink(`#/events/${encodeURIComponent(id)}`, `…${id.slice(-8)}`, { key: id })]),
        )
      : null,
    h(
      "p",
      { class: "error-meta" },
      rec.reason ? h("code", {}, rec.reason) : null,
      rec.reason && rec.traceId ? " · " : null,
      rec.traceId ? ["trace ", h("code", {}, rec.traceId)] : null,
    ),
    h(
      "div",
      { class: "actions" },
      rec.retryable && on.onRetry ? button(model, { mutates: true, onclick: on.onRetry }, "Repeat the request") : null,
      on.onDismiss ? button(model, { mutates: false, onclick: on.onDismiss }, "Dismiss") : null,
    ),
  );
}
