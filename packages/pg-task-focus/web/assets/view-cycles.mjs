// The cycle panel. The running cycle is the focus, with its timer and its
// controls; every cycle that is paused and not stopped stays beneath it,
// dimmed and marked paused in words, with Switch (or Resume when none runs) and
// Stop. Every button names its cycle in its label, and every action sends the
// cycle's id: the page never lets the daemon guess which cycle was meant.

import { boostBox, chosenType, draftOf, startForm } from "./feature-cycles.mjs";
import { durationWords, plural } from "./format.mjs";
import { ranText } from "./present.mjs";
import { cycleTypes, overtimeNow, timerNow } from "./select.mjs";
import { h } from "./vdom.mjs";
import { button, field } from "./view-common.mjs";

/**
 * @param {object} model
 * @param {(name: string, ...args: unknown[]) => unknown} dispatch
 * @param {{compact?: boolean}} [opts] compact leaves out the start form, for the editor area
 */
export function cyclePanel(model, dispatch, { compact = false } = {}) {
  const st = model.state;
  if (!st) return h("aside", { class: "cycle-panel", "aria-labelledby": "cycles-h" }, h("h2", { id: "cycles-h" }, "Cycles"), h("p", { class: "empty" }, "Reading the state…"));
  return h(
    "aside",
    { class: "cycle-panel", "aria-labelledby": "cycles-h" },
    h("h2", { id: "cycles-h" }, "Cycles"),
    st.resume_offer ? resumeOffer(model, dispatch, st.resume_offer) : null,
    st.focus ? focusCard(model, dispatch, st.focus) : h("p", { class: "empty", "data-empty": "focus" }, st.dimmed.length > 0 ? "No cycle is running." : "No cycle is running. Start one below."),
    st.dimmed.length > 0 ? dimmedList(model, dispatch, st.dimmed) : null,
    compact || !st.initialized ? null : startForm_(model, dispatch),
  );
}

function targeted(model, c) {
  return model.route.name === "cycle" && model.route.id === c.id;
}

function resumeOffer(model, dispatch, offer) {
  const c = offer.cycle;
  const isSwitch = offer.action === "switch";
  return h(
    "div",
    { class: "resume-offer", role: "group", "aria-label": "Resume offer", "data-offer": c.id },
    h("p", {}, `Resume ${c.title}?`),
    button(
      model,
      { mutates: true, class: "primary", "aria-label": `${isSwitch ? "Switch to" : "Resume"} ${c.title} (offered)`, onclick: () => dispatch(isSwitch ? "cycle.switch" : "cycle.resume", c) },
      isSwitch ? "Switch to it" : "Resume",
    ),
  );
}

function focusCard(model, dispatch, c) {
  const over = overtimeNow(model, c);
  return h(
    "article",
    { class: `${over ? "cycle focus overtime" : "cycle focus"}${targeted(model, c) ? " targeted" : ""}`, "data-cycle-id": c.id, tabindex: "-1", "aria-labelledby": `cycle-title-${c.id}`, "aria-current": targeted(model, c) ? "true" : undefined },
    h("h3", { id: `cycle-title-${c.id}` }, c.title),
    h(
      "p",
      { class: "state" },
      over ? [h("span", { "aria-hidden": "true" }, "⏰ "), "Overtime"] : [h("span", { "aria-hidden": "true" }, "▶ "), "Running"],
      c.not_in_profile ? " · not in the active profile" : null,
    ),
    h(
      "p",
      { class: "timer", role: "timer", "aria-label": over ? `${c.title}: overtime` : `${c.title}: time remaining` },
      timerNow(model, c),
    ),
    h("p", { class: "meta" }, `Planned ${durationWords(c.planned_minutes * 60)}${c.boost_minutes > 0 ? `, boosted ${durationWords(c.boost_minutes * 60)}` : ""}.`),
    h(
      "div",
      { class: "actions" },
      button(model, { mutates: true, "aria-label": `Pause ${c.title}`, onclick: () => dispatch("cycle.pause", c) }, "Pause"),
      button(model, { mutates: true, class: "danger", "aria-label": `Stop ${c.title}`, onclick: () => dispatch("cycle.stop", c) }, "Stop"),
    ),
    boostRow(model, dispatch, c),
    noteForm(model, dispatch, c, true),
  );
}

function boostRow(model, dispatch, c) {
  const sizes = model.config?.defaults.boost_minutes ?? [];
  return h(
    "div",
    { class: "boost", role: "group", "aria-label": `Boost ${c.title}` },
    sizes.map((m) => button(model, { key: m, mutates: true, "aria-label": `Boost ${c.title} by ${plural(m, "minute")}`, onclick: () => dispatch("cycle.boost", c, m) }, `+${m} min`)),
    h(
      "form",
      {
        class: "inline",
        onsubmit: (e) => {
          e.preventDefault();
          dispatch("boost.custom", c);
        },
      },
      field(model, {
        id: `boost-${c.id}`,
        label: "Other minutes",
        type: "number",
        min: "1",
        step: "1",
        inputmode: "numeric",
        value: boostBox(model, c.id),
        mutates: true,
        oninput: (e) => dispatch("boost.field", c.id, e.target.value),
      }),
      button(model, { mutates: true, type: "submit", "aria-label": `Boost ${c.title} by the minutes entered` }, "Boost"),
    ),
  );
}

function dimmedList(model, dispatch, list) {
  return h(
    "section",
    { class: "dimmed", "aria-labelledby": "paused-h" },
    h("h3", { id: "paused-h" }, "Paused"),
    h(
      "ul",
      { class: "cycles" },
      list.map((c) => {
        const by = c.interrupted_by ? (model.state.focus?.id === c.interrupted_by ? model.state.focus.title : null) : null;
        return h(
          "li",
          { key: c.id, class: `cycle dimmed-cycle${targeted(model, c) ? " targeted" : ""}`, "data-cycle-id": c.id, tabindex: "-1", "aria-labelledby": `cycle-title-${c.id}`, "aria-current": targeted(model, c) ? "true" : undefined },
          h("h4", { id: `cycle-title-${c.id}` }, c.title),
          h("p", { class: "state" }, h("span", { "aria-hidden": "true" }, "⏸ "), "Paused", c.not_in_profile ? " · not in the active profile" : null),
          h("p", { class: "meta" }, ranText(c), by ? `. Interrupted by ${by}.` : "."),
          h(
            "div",
            { class: "actions" },
            c.can_switch
              ? button(model, { mutates: true, class: "primary", "aria-label": `Switch to ${c.title}`, onclick: () => dispatch("cycle.switch", c) }, "Switch")
              : button(model, { mutates: true, class: "primary", "aria-label": `Resume ${c.title}`, onclick: () => dispatch("cycle.resume", c) }, "Resume"),
            button(model, { mutates: true, class: "danger", "aria-label": `Stop ${c.title}`, onclick: () => dispatch("cycle.stop", c) }, "Stop"),
          ),
          h("details", { class: "more" }, h("summary", {}, "Boost and note"), boostRow(model, dispatch, c), noteForm(model, dispatch, c, false)),
        );
      }),
    ),
  );
}

/** The note and key/value form of a cycle. It is optional: stopping never asks for it. */
function noteForm(model, dispatch, c, ownDetails) {
  const d = draftOf(model, c);
  const form = h(
    "form",
    {
      class: "note-form",
      onsubmit: (e) => {
        e.preventDefault();
        dispatch("note.save", c);
      },
    },
    field(model, {
      id: `note-${c.id}`,
      label: "Note",
      tag: "textarea",
      rows: "3",
      value: d.note,
      mutates: true,
      oninput: (e) => dispatch("note.field", c, e.target.value),
    }),
    h(
      "fieldset",
      { class: "pairs" },
      h("legend", {}, "Key and value pairs"),
      d.kv.map((p, i) =>
        h(
          "div",
          { key: i, class: "pair" },
          field(model, { id: `kv-key-${c.id}-${i}`, label: "Key", type: "text", value: p.key, pattern: "[a-z0-9_-]+", mutates: true, oninput: (e) => dispatch("note.kv", c, i, "key", e.target.value) }),
          field(model, { id: `kv-value-${c.id}-${i}`, label: "Value", type: "text", value: p.value, mutates: true, oninput: (e) => dispatch("note.kv", c, i, "value", e.target.value) }),
          button(model, { mutates: true, "aria-label": `Remove pair ${i + 1}`, onclick: () => dispatch("note.remove", c, i) }, "Remove"),
        ),
      ),
      button(model, { mutates: true, onclick: () => dispatch("note.add", c) }, "Add a pair"),
    ),
    button(model, { mutates: true, type: "submit", "aria-label": `Save the note of ${c.title}` }, "Save note"),
  );
  return ownDetails ? h("details", { class: "more" }, h("summary", {}, "Note and details"), form) : form;
}

function startForm_(model, dispatch) {
  const f = startForm(model);
  const types = cycleTypes(model);
  const running = model.state.focus;
  const chosen = types.find((t) => t.id === chosenType(model));
  return h(
    "form",
    {
      class: "start-form",
      "aria-label": "Start a cycle",
      onsubmit: (e) => {
        e.preventDefault();
        dispatch("cycleStart.submit");
      },
    },
    h("h3", {}, "Start a cycle"),
    types.length === 0
      ? h("p", { class: "empty" }, "No cycle types are configured.")
      : [
          field(
            model,
            { id: "start-type", label: "Type", tag: "select", value: chosenType(model), mutates: true, onchange: (e) => dispatch("cycleStart.field", "type", e.target.value) },
            types.map((t) => h("option", { key: t.id, value: t.id }, `${t.title}${t.inProfile ? "" : " (not in the active profile)"}`)),
          ),
          field(model, {
            id: "start-minutes",
            label: "Minutes",
            hint: chosen ? `Leave empty for ${chosen.minutes}.` : undefined,
            type: "number",
            min: "1",
            step: "1",
            inputmode: "numeric",
            value: f.minutes,
            mutates: true,
            oninput: (e) => dispatch("cycleStart.field", "minutes", e.target.value),
          }),
          f.problem ? h("p", { class: "problem", role: "alert" }, f.problem) : null,
          running ? h("p", { class: "hint", "data-hint": "interrupt" }, `Starting a cycle pauses ${running.title}; it stays below, dimmed, and you can switch back.`) : null,
          button(model, { mutates: true, type: "submit", class: "primary" }, "Start cycle"),
        ],
  );
}
