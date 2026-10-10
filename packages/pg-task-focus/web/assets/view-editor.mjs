// The editor area: the events as a table in the corrected view, with the
// original one action away; correct and retract; and the two named operations
// with a before-and-after timeline. There is no delete: a retraction is an
// event too, and the editor says how far it reaches.

import { cyclesOf, editorState, operationPreview } from "./feature-editor.mjs";
import { batchOf, batchSize, editableFields, effectiveMs, isCorrectable, isRetractable, lookupOf, sentence, shortId } from "./events.mjs";
import { durationWords } from "./format.mjs";
import { cycleTypes, displayZone } from "./select.mjs";
import { h } from "./vdom.mjs";
import { button, control, errorBox, field } from "./view-common.mjs";
import { clockInZone, dateInZone, dateTimeInZone } from "./zone.mjs";

const TYPE_CHOICES = [
  ["", "All events"],
  ["task.completed", "Tasks completed"],
  ["task.skipped", "Tasks skipped"],
  ["task.missed", "Tasks missed"],
  ["cycle.started", "Cycles started"],
  ["cycle.stopped", "Cycles stopped"],
  ["cycle.annotated", "Cycle notes"],
  ["period.changed", "Period changes"],
  ["event.corrected", "Corrections"],
  ["event.retracted", "Retractions"],
];

const RETRACT_LIMIT = "Refused once later events depend on it; the refusal names them.";

export function editorArea(model, dispatch) {
  const zone = displayZone(model);
  const ed = editorState(model);
  if (!model.state) return h("p", { class: "empty" }, "Reading the state…");
  if (!ed) return h("p", { class: "empty" }, "Reading the events…");
  const items = ed.items ?? [];
  const look = lookupOf(items, model.state);
  return h(
    "div",
    { class: "editor" },
    h("h2", { id: "editor-h" }, "Editor"),
    h("p", { class: "hint" }, `Nothing is ever deleted: a fix adds an event that takes an earlier one back, and the original stays in the log. Times are in ${zone}.`),
    operationsBar(model, dispatch, ed),
    ed.op ? operationForm(model, dispatch, ed, zone) : null,
    filters(model, dispatch, ed),
    ed.error ? h("p", { class: "problem", role: "alert" }, ed.error.detail ?? "The events could not be read.") : null,
    ed.missing ? h("p", { class: "problem", role: "alert" }, `The log has no event ${shortId(ed.missing)}.`) : null,
    ed.loading && ed.items === null ? h("p", { class: "empty" }, "Reading the events…") : null,
    ed.items !== null && items.length === 0 ? h("p", { class: "empty", "data-empty": "events" }, "No events match these filters. Show all of the log to see more.") : null,
    items.length > 0 ? table(model, dispatch, ed, items, look, zone) : null,
  );
}

function operationsBar(model, dispatch, ed) {
  return h(
    "div",
    { class: "operations", role: "group", "aria-label": "Fix a cycle" },
    button(model, { mutates: true, "aria-pressed": ed.op?.kind === "break" ? "true" : "false", onclick: () => dispatch("op.open", "break") }, "Insert break (from, to)"),
    button(model, { mutates: true, "aria-pressed": ed.op?.kind === "endat" ? "true" : "false", onclick: () => dispatch("op.open", "endat") }, "End at (time)"),
  );
}

function filters(model, dispatch, ed) {
  return h(
    "form",
    { class: "filters", "aria-label": "Filter the events", onsubmit: (e) => e.preventDefault() },
    h(
      "fieldset",
      { class: "view-toggle" },
      h("legend", {}, "View"),
      h("label", {}, control(model, "input", { mutates: false, type: "radio", name: "view", value: "corrected", checked: ed.view === "corrected", onchange: () => dispatch("editor.setView", "corrected") }), " Corrected"),
      h("label", {}, control(model, "input", { mutates: false, type: "radio", name: "view", value: "original", checked: ed.view === "original", onchange: () => dispatch("editor.setView", "original") }), " As first logged"),
    ),
    field(model, { id: "ed-from", label: "From (date)", type: "date", value: ed.from, mutates: false, onchange: (e) => dispatch("editor.filter", "from", e.target.value) }),
    field(model, { id: "ed-to", label: "To (date)", type: "date", value: ed.to, mutates: false, onchange: (e) => dispatch("editor.filter", "to", e.target.value) }),
    field(
      model,
      { id: "ed-type", label: "Show", tag: "select", value: ed.type, mutates: false, onchange: (e) => dispatch("editor.filter", "type", e.target.value) },
      TYPE_CHOICES.map(([v, l]) => h("option", { key: v, value: v }, l)),
    ),
    button(model, { mutates: false, onclick: () => dispatch("editor.showAll") }, "Show all of the log"),
  );
}

function table(model, dispatch, ed, items, look, zone) {
  return h(
    "table",
    { class: "events" },
    h("caption", {}, `${items.length} ${items.length === 1 ? "event" : "events"}, ${ed.view === "corrected" ? "as corrected" : "as first logged"}`),
    h("thead", {}, h("tr", {}, h("th", { scope: "col" }, "When"), h("th", { scope: "col" }, "What"), h("th", { scope: "col" }, "State"), h("th", { scope: "col" }, "Actions"))),
    h("tbody", {}, items.flatMap((it) => rows(model, dispatch, ed, it, items, look, zone))),
  );
}

function rows(model, dispatch, ed, it, items, look, zone) {
  const e = it.event;
  const ms = effectiveMs(it);
  const batch = batchOf(it);
  const corrected = it.corrected_by.length > 0;
  const flags = [];
  if (it.retracted) flags.push("Retracted");
  if (corrected) flags.push(`Corrected ${it.corrected_by.length === 1 ? "once" : `${it.corrected_by.length} times`}`);
  if (flags.length === 0) flags.push("Live");
  const highlighted = model.route.name === "events" && model.route.id === it.id;
  const main = h(
    "tr",
    { key: it.id, class: `event${it.retracted ? " retracted" : ""}${highlighted ? " highlighted" : ""}`, "data-event-id": it.id, tabindex: "-1", "aria-current": highlighted ? "true" : undefined },
    h("td", {}, h("time", { datetime: e.effective_at }, dateTimeInZone(ms, zone)), h("span", { class: "sub" }, ` ${zone}`)),
    h("td", {}, h("span", { class: "type" }, e.type), " ", sentence(it, look, zone), batch ? h("span", { class: "sub" }, ` · batch ${shortId(batch)}, ${batchSize(items, batch)} events`) : null),
    h("td", {}, flags.join(", ")),
    h("td", { class: "row-actions" }, rowActions(model, dispatch, ed, it, batch, corrected)),
  );
  const out = [main];
  if (ed.showOriginal[it.id]) out.push(originalRow(it, look, zone, ed.view));
  if (ed.correct?.id === it.id) out.push(correctRow(model, dispatch, ed, it, zone));
  if (ed.rowError?.id === it.id) out.push(h("tr", { key: `${it.id}:err`, class: "row-error" }, h("td", { colspan: "4" }, errorBox(model, ed.rowError.record, { onDismiss: () => dispatch("editor.dismissRowError") }))));
  return out;
}

function rowActions(model, dispatch, ed, it, batch, corrected) {
  const t = it.event.type;
  const name = `${t} ${shortId(it.id)}`;
  return [
    corrected || ed.view === "original"
      ? button(model, { mutates: false, "aria-expanded": ed.showOriginal[it.id] ? "true" : "false", "aria-label": `${ed.showOriginal[it.id] ? "Hide" : "Show"} the original of ${name}`, onclick: () => dispatch("editor.toggleOriginal", it.id) }, ed.showOriginal[it.id] ? "Hide original" : "Show original")
      : null,
    isCorrectable(t) && !it.retracted && ed.view === "corrected"
      ? button(model, { mutates: true, "aria-label": `Correct ${name}`, onclick: () => dispatch("editor.openCorrect", it) }, "Correct")
      : null,
    isRetractable(t) && !it.retracted && ed.view === "corrected"
      ? [
          button(model, { mutates: true, "aria-label": `${batch ? "Retract the batch of" : "Retract"} ${name}`, "aria-describedby": `limit-${it.id}`, onclick: () => dispatch("editor.retract", it) }, batch ? "Retract batch" : "Retract"),
          h("span", { class: "sub limit", id: `limit-${it.id}` }, RETRACT_LIMIT),
        ]
      : null,
  ];
}

function originalRow(it, look, zone, view) {
  const orig = it.original ?? it.event;
  const ms = Date.parse(orig.effective_at);
  return h(
    "tr",
    { key: `${it.id}:orig`, class: "original" },
    h(
      "td",
      { colspan: "4" },
      h("p", {}, h("strong", {}, view === "corrected" ? "As first logged: " : "As logged: "), `${dateTimeInZone(ms, zone)} ${zone}, `, sentence({ ...it, event: orig }, look, zone)),
      it.corrected_by.length > 0 ? h("p", { class: "sub" }, `Corrected by ${it.corrected_by.map(shortId).join(", ")}.`) : null,
      it.retracted ? h("p", { class: "sub" }, `Retracted by ${shortId(it.retracted_by ?? "")}.`) : null,
    ),
  );
}

function correctRow(model, dispatch, ed, it, zone) {
  const c = ed.correct;
  const types = cycleTypes(model);
  const fields = [{ name: "effective_at", label: `Time (${zone})`, kind: "instant" }, ...editableFields(it.event.type, it.event.data)];
  return h(
    "tr",
    { key: `${it.id}:fix`, class: "correct" },
    h(
      "td",
      { colspan: "4" },
      h(
        "form",
        {
          class: "correct-form",
          "aria-label": `Correct ${it.event.type}`,
          onsubmit: (e) => {
            e.preventDefault();
            dispatch("editor.submitCorrect");
          },
        },
        fields.map((f) => correctField(model, dispatch, it, f, c, types, zone)),
        it.event.type === "cycle.started" ? h("p", { class: "hint" }, "The title follows the type: the service fills it from the configuration.") : null,
        field(model, { id: `reason-${it.id}`, label: "Why (optional)", type: "text", value: c.reason, mutates: true, oninput: (e) => dispatch("editor.correctReason", e.target.value) }),
        c.problem ? h("p", { class: "problem", role: "alert" }, c.problem) : null,
        c.error ? errorBox(model, c.error, { onRetry: () => dispatch("editor.submitCorrect") }) : null,
        h("div", { class: "actions" }, button(model, { mutates: true, type: "submit", class: "primary" }, "Save correction"), button(model, { mutates: false, onclick: () => dispatch("editor.cancelCorrect") }, "Cancel")),
      ),
    ),
  );
}

function correctField(model, dispatch, it, f, c, types, zone) {
  const id = `fix-${it.id}-${f.name}`;
  const v = c.values[f.name];
  switch (f.kind) {
    case "instant":
      return field(model, { id, label: f.label.includes("(") ? f.label : `${f.label} (${zone})`, type: "datetime-local", step: "1", value: v, mutates: true, oninput: (e) => dispatch("editor.correctField", f.name, e.target.value) });
    case "number":
      return field(model, { id, label: f.label, type: "number", min: "1", step: "1", value: v, mutates: true, oninput: (e) => dispatch("editor.correctField", f.name, e.target.value) });
    case "date":
      return field(model, { id, label: f.label, type: "date", value: v, mutates: true, oninput: (e) => dispatch("editor.correctField", f.name, e.target.value) });
    case "longtext":
      return field(model, { id, label: f.label, tag: "textarea", rows: "3", value: v, mutates: true, oninput: (e) => dispatch("editor.correctField", f.name, e.target.value) });
    case "cycle-type": {
      const known = types.some((t) => t.id === v);
      return field(
        model,
        { id, label: f.label, tag: "select", value: v, mutates: true, onchange: (e) => dispatch("editor.correctField", f.name, e.target.value) },
        known ? null : h("option", { key: v, value: v }, `${v} (not configured)`),
        types.map((t) => h("option", { key: t.id, value: t.id }, `${t.title} (${t.id})`)),
      );
    }
    case "kv":
      return h(
        "fieldset",
        { key: f.name, class: "pairs" },
        h("legend", {}, f.label),
        v.map((p, i) =>
          h(
            "div",
            { key: i, class: "pair" },
            field(model, { id: `${id}-k${i}`, label: "Key", type: "text", value: p.key, mutates: true, oninput: (e) => dispatch("editor.correctKv", f.name, i, "key", e.target.value) }),
            field(model, { id: `${id}-v${i}`, label: "Value", type: "text", value: p.value, mutates: true, oninput: (e) => dispatch("editor.correctKv", f.name, i, "value", e.target.value) }),
            button(model, { mutates: true, "aria-label": `Remove pair ${i + 1}`, onclick: () => dispatch("editor.correctKvRemove", f.name, i) }, "Remove"),
          ),
        ),
        button(model, { mutates: true, onclick: () => dispatch("editor.correctKvAdd", f.name) }, "Add a pair"),
      );
    default:
      return field(model, { id, label: f.label, type: "text", value: v, mutates: true, oninput: (e) => dispatch("editor.correctField", f.name, e.target.value) });
  }
}

function operationForm(model, dispatch, ed, zone) {
  const op = ed.op;
  const cycles = cyclesOf(ed.cycleItems);
  const { preview, rec, problem } = operationPreview(model);
  const isBreak = op.kind === "break";
  const title = isBreak ? "Insert break" : "End at";
  return h(
    "form",
    {
      class: "operation",
      "aria-label": title,
      onsubmit: (e) => {
        e.preventDefault();
        dispatch("op.submit");
      },
    },
    h("h3", {}, isBreak ? "Insert a break a cycle forgot" : "End a cycle at an earlier time"),
    field(
      model,
      { id: "op-cycle", label: "Cycle", tag: "select", value: op.cycleId, mutates: true, onchange: (e) => dispatch("op.field", "cycleId", e.target.value) },
      cycles.length === 0 ? h("option", { value: "" }, "No cycles in the log") : null,
      cycles.map((c) => h("option", { key: c.id, value: c.id }, `${c.title}, started ${dateTimeInZone(c.startedAt, zone)}`)),
    ),
    isBreak
      ? [
          field(model, { id: "op-from", label: `Break started (${zone})`, type: "datetime-local", step: "1", value: op.from, mutates: true, oninput: (e) => dispatch("op.field", "from", e.target.value) }),
          field(model, { id: "op-to", label: `Break ended (${zone})`, type: "datetime-local", step: "1", value: op.to, mutates: true, oninput: (e) => dispatch("op.field", "to", e.target.value) }),
        ]
      : field(model, { id: "op-at", label: `The cycle really ended (${zone})`, type: "datetime-local", step: "1", value: op.at, mutates: true, oninput: (e) => dispatch("op.field", "at", e.target.value) }),
    problem ? h("p", { class: "hint", "data-op-problem": "true" }, problem) : null,
    preview ? timeline(preview, rec, zone) : null,
    op.problem ? h("p", { class: "problem", role: "alert" }, op.problem) : null,
    op.error ? errorBox(model, op.error, { onRetry: () => dispatch("op.submit") }) : null,
    h(
      "div",
      { class: "actions" },
      button(model, { mutates: true, type: "submit", class: "primary", disabled: !preview }, isBreak ? "Insert break" : "End the cycle"),
      button(model, { mutates: false, onclick: () => dispatch("op.cancel") }, "Cancel"),
    ),
  );
}

/** The before and after of a change: each running segment with its date and zone, and the running time. */
function timeline(preview, rec, zone) {
  const seg = (s) => `${dateInZone(s.start, zone)} ${clockInZone(s.start, zone)} to ${s.end === null ? "now" : `${dateInZone(s.end, zone) === dateInZone(s.start, zone) ? "" : `${dateInZone(s.end, zone)} `}${clockInZone(s.end, zone)}`}`;
  const col = (label, side) =>
    h(
      "div",
      { class: "timeline-col", "data-timeline": label.toLowerCase() },
      h("h4", {}, label),
      side.segments.length === 0 ? h("p", { class: "empty" }, "No running time.") : h("ul", {}, side.segments.map((s, i) => h("li", { key: i }, seg(s)))),
      h("p", { class: "total" }, `Running time ${durationWords(side.ms / 1000)}`),
    );
  return h(
    "div",
    { class: "timeline", role: "group", "aria-label": "Before and after" },
    h("p", { class: "hint" }, `${rec.title}, times in ${zone}. The service checks the change when you confirm it, and refuses it with its reason if it cannot be done.`),
    h("div", { class: "timeline-cols" }, col("Before", preview.before), col("After", preview.after)),
    preview.unchanged ? h("p", { class: "hint" }, "This would change nothing.") : null,
  );
}
