// The period modal and the profile modal, as native <dialog> elements opened
// with showModal(): the browser traps focus, closes on Escape and gives focus
// back. Each shows the daemon's preview before it can be confirmed.

import { blockingCycles, periodState, previewIsCurrent, profileState, rolloverProblems } from "./feature-periods.mjs";
import { isBlank, plural } from "./format.mjs";
import { KINDS, KIND_TITLES, displayZone, isReadOnly, readOnlySentence } from "./select.mjs";
import { h } from "./vdom.mjs";
import { button, control, errorBox, field } from "./view-common.mjs";
import { knownZones } from "./zone.mjs";

const UNDO_LIMIT = "Undo is refused once later events depend on the new periods or their tasks; the refusal names those events.";

export function dialogs(model, dispatch) {
  const p = model.ui.modal === "period" ? periodState(model) : null;
  const pr = model.ui.modal === "profile" ? profileState(model) : null;
  return [
    h(
      "dialog",
      { key: "period", id: "period-dialog", class: "modal", modal: !!p, "aria-labelledby": p ? "period-h" : undefined, onclose: () => model.ui.modal === "period" && dispatch("period.close") },
      p ? periodModal(model, dispatch, p) : null,
    ),
    h(
      "dialog",
      { key: "profile", id: "profile-dialog", class: "modal", modal: !!pr, "aria-labelledby": pr ? "profile-h" : undefined, onclose: () => model.ui.modal === "profile" && dispatch("profile.close") },
      pr ? profileModal(model, dispatch, pr) : null,
    ),
  ];
}

/** The READ-ONLY sentence inside a dialog: the banner behind a modal dialog is not reachable, so the dialog says it too. */
function readOnlyNote(model) {
  const s = readOnlySentence(model.state?.store);
  return s ? h("p", { class: "banner banner-readonly", role: "alert", "data-banner": "read-only-dialog" }, s) : null;
}

function taskList(list, empty) {
  if (list.length === 0) return h("p", { class: "empty" }, empty);
  return h("ul", {}, list.map((t) => h("li", { key: t.id ?? t.definition }, t.title ?? t.definition, t.overdue ? h("span", { class: "overdue" }, [" ", h("span", { "aria-hidden": "true" }, "⚠ "), "would be overdue at once"]) : null)));
}

// ---- the period modal ----

function periodModal(model, dispatch, p) {
  const zone = displayZone(model);
  const result = previewIsCurrent(p) ? p.preview.result : null;
  const blockers = blockingCycles(model);
  const leaving = result?.preview.leaving ?? [];
  const rollProblems = result ? rolloverProblems(p.rollover, leaving) : [];
  const canConfirm = !!result && blockers.length === 0 && rollProblems.length === 0 && !isReadOnly(model) && model.busy === 0;
  const zones = knownZones();
  return h(
    "form",
    {
      class: "modal-body",
      method: "dialog",
      onsubmit: (e) => {
        e.preventDefault();
        dispatch(result ? "period.confirm" : "period.preview");
      },
    },
    h("h2", { id: "period-h" }, model.state.initialized ? "Change periods" : "Set your day, week and sprint"),
    readOnlyNote(model),
    h("p", { class: "hint" }, "Choose the periods to begin. The preview shows what becomes of every open task before anything is written."),
    KINDS.map((k) => kindFields(model, dispatch, p, k)),
    h(
      "fieldset",
      { class: "common" },
      h("legend", {}, "For all of them"),
      field(model, { id: "period-tz", label: "Zone (from this browser; you can change it)", type: "text", list: "zones", value: p.form.tz, mutates: true, autocomplete: "off", oninput: (e) => dispatch("period.field", "tz", e.target.value) }),
      h("datalist", { id: "zones" }, zones.map((z) => h("option", { key: z, value: z }))),
      field(
        model,
        { id: "period-profile", label: "Profile", tag: "select", value: p.form.profile, mutates: true, onchange: (e) => dispatch("period.field", "profile", e.target.value) },
        h("option", { value: "" }, `Keep ${model.state.profile || "the default"}`),
        (model.config?.profiles ?? []).map((pf) => h("option", { key: pf.name, value: pf.name }, pf.name)),
      ),
      field(model, {
        id: "period-backdate",
        label: `Back-date the change to (${p.form.tz.trim() || zone}; empty is now)`,
        hint: "There is no limit on how far back. The service still refuses a time that would make the log impossible.",
        type: "datetime-local",
        step: "1",
        value: p.form.backdate,
        mutates: true,
        oninput: (e) => dispatch("period.field", "backdate", e.target.value),
      }),
    ),
    blockers.length > 0 ? blockerList(model, dispatch, p, blockers, zone) : null,
    p.problems.length > 0 ? h("ul", { class: "problems", role: "alert" }, p.problems.map((x) => h("li", { key: x }, x))) : null,
    p.stale ? h("p", { class: "banner banner-notice", role: "status" }, "The log changed after the preview, so the preview was read again. Check it before confirming.") : null,
    p.error ? errorBox(model, p.error, { onRetry: () => dispatch("period.confirm") }) : null,
    p.previewing ? h("p", { class: "empty", role: "status" }, "Reading the preview…") : null,
    result ? previewView(model, dispatch, p, result, rollProblems) : null,
    h("p", { class: "hint" }, UNDO_LIMIT),
    h(
      "div",
      { class: "actions" },
      button(model, { mutates: false, type: "button", onclick: () => dispatch("period.preview"), disabled: p.previewing }, result ? "Refresh the preview" : "Preview"),
      button(model, { mutates: true, type: "submit", class: "primary", disabled: !canConfirm || !result }, "Confirm the change"),
      button(model, { mutates: false, type: "button", onclick: () => dispatch("period.close") }, "Close"),
    ),
  );
}

function kindFields(model, dispatch, p, kind) {
  const f = p.form.kinds[kind];
  const cur = model.state.periods.find((x) => x.kind === kind);
  return h(
    "fieldset",
    { key: kind, class: "kind" },
    h("legend", {}, KIND_TITLES[kind]),
    h(
      "label",
      { class: "check" },
      control(model, "input", { mutates: true, type: "checkbox", id: `kind-${kind}-on`, checked: f.on, onchange: (e) => dispatch("period.field", `kinds.${kind}.on`, e.target.checked) }),
      ` Begin a new ${kind}`,
      cur?.ended ? h("span", { class: "sub" }, ` (${cur.banner})`) : null,
    ),
    f.on
      ? h(
          "div",
          { class: "kind-fields" },
          field(model, { id: `kind-${kind}-start`, label: "First day", type: "date", value: f.start, mutates: true, oninput: (e) => dispatch("period.field", `kinds.${kind}.start`, e.target.value) }),
          kind !== "day" ? field(model, { id: `kind-${kind}-end`, label: "Last day", type: "date", value: f.end, mutates: true, oninput: (e) => dispatch("period.field", `kinds.${kind}.end`, e.target.value) }) : null,
          field(model, { id: `kind-${kind}-label`, label: "Label (optional)", type: "text", value: f.label, mutates: true, autocomplete: "off", oninput: (e) => dispatch("period.field", `kinds.${kind}.label`, e.target.value) }),
        )
      : null,
  );
}

function blockerList(model, dispatch, p, blockers, zone) {
  return h(
    "section",
    { class: "blockers", role: "alert", "aria-labelledby": "blockers-h" },
    h("h3", { id: "blockers-h" }, "A cycle is not stopped"),
    h("p", {}, `${plural(blockers.length, "cycle")} must be stopped before the periods can change. Stop it now, or end it at the time it really ended; no time is filled in for you.`),
    h(
      "ul",
      {},
      blockers.map((c) =>
        h(
          "li",
          { key: c.id, class: "blocker", "data-cycle-id": c.id },
          h("strong", {}, c.title),
          ` (${c.status === "running" ? "running" : c.status === "paused" ? "paused" : c.status})`,
          h("div", { class: "actions" }, button(model, { mutates: true, "aria-label": `Stop ${c.title} now`, onclick: () => dispatch("blocker.stop", c) }, "Stop")),
          h(
            "div",
            { class: "end-at" },
            field(model, {
              id: `end-at-${c.id}`,
              label: `End ${c.title} at (${zone})`,
              type: "datetime-local",
              step: "1",
              value: p.endAt[c.id] ?? "",
              mutates: true,
              oninput: (e) => dispatch("blocker.endAtField", c.id, e.target.value),
            }),
            button(model, { mutates: true, "aria-label": `End ${c.title} at the time entered`, onclick: () => dispatch("blocker.endAt", c) }, "End at"),
            p.endAtProblem[c.id] ? h("p", { class: "problem", role: "alert" }, p.endAtProblem[c.id]) : null,
          ),
        ),
      ),
    ),
  );
}

function previewView(model, dispatch, p, result, rollProblems) {
  const pv = result.preview;
  const r = p.rollover;
  return h(
    "section",
    { class: "preview", "aria-labelledby": "preview-h" },
    h("h3", { id: "preview-h" }, "What will happen"),
    h("h4", {}, "Open tasks in the periods being left"),
    pv.leaving.length === 0
      ? h("p", { class: "empty" }, "No open tasks are left behind.")
      : [
          h(
            "div",
            { class: "actions" },
            button(model, { mutates: true, "aria-pressed": r.mode === "missed" ? "true" : "false", onclick: () => dispatch("rollover.mode", "missed") }, "Mark all missed"),
            button(model, { mutates: true, "aria-pressed": r.mode === "skipAll" ? "true" : "false", onclick: () => dispatch("rollover.mode", "skipAll") }, "Skip all with one reason"),
          ),
          r.mode === "skipAll"
            ? field(model, { id: "skip-all-reason", label: "Reason for skipping all of them", type: "text", value: r.skipAllReason, mutates: true, required: true, autocomplete: "off", oninput: (e) => dispatch("rollover.reason", e.target.value) })
            : null,
          h(
            "ul",
            { class: "leaving" },
            pv.leaving.map((t) => {
              const per = r.perTask[t.id] ?? { skip: false, reason: "" };
              const skipping = r.mode === "skipAll" || per.skip;
              return h(
                "li",
                { key: t.id, "data-task-id": t.id },
                h("span", { class: "title" }, t.title),
                t.overdue ? h("span", { class: "overdue" }, [" ", h("span", { "aria-hidden": "true" }, "⚠ "), "overdue"]) : null,
                h("span", { class: "fate" }, ` → ${skipping ? "will be skipped" : "will be marked missed"}`),
                r.mode === "missed"
                  ? h("label", { class: "check" }, control(model, "input", { mutates: true, type: "checkbox", id: `skip-${t.id}`, checked: per.skip, onchange: (e) => dispatch("rollover.task", t.id, e.target.checked, undefined) }), " Skip instead")
                  : h("label", { class: "check" }, control(model, "input", { mutates: true, type: "checkbox", id: `own-${t.id}`, checked: per.skip, onchange: (e) => dispatch("rollover.task", t.id, e.target.checked, undefined) }), " Use its own reason"),
                per.skip
                  ? field(model, { id: `reason-${t.id}`, label: `Reason for skipping ${t.title}`, type: "text", value: per.reason, mutates: true, autocomplete: "off", oninput: (e) => dispatch("rollover.task", t.id, undefined, e.target.value) })
                  : null,
              );
            }),
          ),
        ],
    h("h4", {}, "Tasks the new periods will add"),
    taskList(pv.materialize, "No tasks will be added."),
    pv.not_materialized.length > 0
      ? [h("h4", {}, "Tasks that cannot be added"), h("ul", {}, pv.not_materialized.map((n) => h("li", { key: n.definition }, `${n.definition}: ${n.reason}`)))]
      : null,
    pv.profile_add.length + pv.profile_withdraw.length + pv.profile_reinstate.length > 0
      ? [
          h("h4", {}, "Profile change on the periods that stay"),
          h("p", {}, "Added:"),
          taskList(pv.profile_add, "None."),
          h("p", {}, "Withdrawn:"),
          taskList(pv.profile_withdraw, "None."),
          h("p", {}, "Reinstated:"),
          taskList(pv.profile_reinstate, "None."),
        ]
      : null,
    rollProblems.length > 0 ? h("ul", { class: "problems", role: "status" }, rollProblems.map((x) => h("li", { key: x }, x))) : null,
  );
}

// ---- the profile modal ----

function profileModal(model, dispatch, p) {
  const result = p.preview && p.preview.profile === p.selected ? p.preview.result : null;
  const pv = result?.preview;
  return h(
    "form",
    {
      class: "modal-body",
      method: "dialog",
      onsubmit: (e) => {
        e.preventDefault();
        dispatch(result ? "profile.confirm" : "profile.preview");
      },
    },
    h("h2", { id: "profile-h" }, "Change profile"),
    readOnlyNote(model),
    h("p", { class: "hint" }, "A profile change leaves tasks that are done, skipped or missed alone. It adds, withdraws and reinstates tasks in the periods that stay."),
    field(
      model,
      { id: "profile-select", label: "Profile", tag: "select", value: p.selected, mutates: true, onchange: (e) => dispatch("profile.select", e.target.value) },
      (model.config?.profiles ?? []).map((pf) => h("option", { key: pf.name, value: pf.name }, pf.name === model.state.profile ? `${pf.name} (active)` : pf.name)),
    ),
    p.error ? errorBox(model, p.error, { onRetry: () => dispatch("profile.confirm") }) : null,
    p.previewing ? h("p", { class: "empty", role: "status" }, "Reading the preview…") : null,
    pv
      ? h(
          "section",
          { class: "preview", "aria-label": "What will happen" },
          h("h3", {}, "What will happen"),
          h("h4", {}, "Added"),
          taskList(pv.profile_add, "No tasks are added."),
          h("h4", {}, "Withdrawn"),
          taskList(pv.profile_withdraw, "No tasks are withdrawn."),
          h("h4", {}, "Reinstated"),
          taskList(pv.profile_reinstate, "No tasks are reinstated."),
        )
      : null,
    h("p", { class: "hint" }, "Undo is refused once later events depend on the change; the refusal names them."),
    h(
      "div",
      { class: "actions" },
      button(model, { mutates: false, onclick: () => dispatch("profile.preview"), disabled: p.previewing || isBlank(p.selected) }, result ? "Refresh the preview" : "Preview"),
      button(model, { mutates: true, type: "submit", class: "primary", disabled: !result || model.busy > 0 }, "Confirm the change"),
      button(model, { mutates: false, onclick: () => dispatch("profile.close") }, "Close"),
    ),
  );
}
