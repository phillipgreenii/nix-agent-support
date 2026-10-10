// The checklists: one section per current period, each task with its status in
// words, its due time and zone, and, while it can still be resolved, Done,
// "Done at" and Skip.

import { isBlank, plural, relativeDue, safeHref } from "./format.mjs";
import { dueText, statusIcon, statusText } from "./present.mjs";
import { KIND_TITLES, checklists, displayZone, isActionable } from "./select.mjs";
import { h } from "./vdom.mjs";
import { button, field } from "./view-common.mjs";
import { taskForm } from "./feature-tasks.mjs";
import { parseInstant } from "./zone.mjs";

export function checklistArea(model, dispatch) {
  const st = model.state;
  if (!st) return h("p", { class: "empty" }, "Reading the state…");
  if (!st.initialized) {
    return h(
      "div",
      { class: "empty-state" },
      h("p", {}, "Nothing is set up yet. Set the day, week and sprint, and the tasks of the active profile appear here."),
      button(model, { mutates: true, onclick: () => dispatch("period.open") }, "Set up the day, week and sprint"),
    );
  }
  const lists = checklists(st);
  return h(
    "div",
    { class: "checklists" },
    lists.map((l) => section(model, dispatch, l)),
    h("datalist", { id: "recent-reasons" }, (model.ui.reasons ?? []).map((r) => h("option", { key: r, value: r }))),
  );
}

function section(model, dispatch, { period, groups }) {
  const hid = `period-${period.kind}`;
  const count = groups.reduce((n, g) => n + g.tasks.length, 0);
  const open = groups.reduce((n, g) => n + g.tasks.filter((t) => t.status === "open").length, 0);
  return h(
    "section",
    { key: period.kind, class: period.ended ? "period-section ended" : "period-section", "aria-labelledby": hid },
    h("h2", { id: hid }, `${KIND_TITLES[period.kind]}: ${period.kind === "day" ? period.start : `${period.start} to ${period.end}`}`),
    period.ended
      ? h("p", { class: "ended-note" }, `This ${period.kind} has ended. ${period.banner}. Its tasks can still be completed until it is rolled over.`)
      : null,
    count === 0
      ? h("p", { class: "empty" }, `No tasks in this ${period.kind} for the active profile.`)
      : [
          h("p", { class: "summary" }, `${plural(open, "task")} open of ${count}.`),
          groups.map((g) => h("div", { key: `${period.kind}:${g.name}`, class: "task-group" }, g.name ? h("h3", {}, g.name) : null, h("ul", { class: "tasks" }, g.tasks.map((t) => taskRow(model, dispatch, t))))),
        ],
  );
}

function taskRow(model, dispatch, task) {
  const form = taskForm(model);
  const mine = form && form.taskId === task.id ? form : null;
  const href = task.link ? safeHref(task.link) : null;
  const rel = task.status === "open" ? relativeDue(parseInstant(task.due) ?? model.now, model.now) : null;
  return h(
    "li",
    {
      key: task.id,
      class: `task task-${task.status}${task.overdue ? " task-overdue" : ""}${model.route.name === "task" && model.route.id === task.id ? " targeted" : ""}`,
      "data-task-id": task.id,
      tabindex: "-1",
      "aria-current": model.route.name === "task" && model.route.id === task.id ? "true" : undefined,
    },
    h(
      "div",
      { class: "task-main" },
      h("span", { class: "status" }, h("span", { "aria-hidden": "true" }, `${statusIcon(task)} `), statusText(task, model.now)),
      h("span", { class: "title" }, href ? h("a", { href, rel: "noopener noreferrer", target: "_blank" }, task.title) : task.title),
      h("span", { class: "due" }, `due ${dueText(task, model.now)}`),
      rel?.overdue ? h("span", { class: "overdue" }, h("span", { "aria-hidden": "true" }, "⚠ "), rel.text) : null,
      task.status === "skipped" && task.reason ? h("span", { class: "reason" }, `Reason: ${task.reason}`) : null,
      task.status === "withdrawn" ? h("span", { class: "hint" }, "Not in the active profile.") : null,
    ),
    isActionable(task) && !mine
      ? h(
          "div",
          { class: "task-actions" },
          button(model, { mutates: true, class: "primary", "aria-label": `Done: ${task.title}`, onclick: () => dispatch("task.done", task) }, "Done"),
          button(model, { mutates: true, "aria-label": `Done at a time: ${task.title}`, onclick: () => dispatch("task.openDoneAt", task) }, "Done at…"),
          button(model, { mutates: true, "aria-label": `Skip: ${task.title}`, onclick: () => dispatch("task.openSkip", task) }, "Skip…"),
        )
      : null,
    mine ? taskFormView(model, dispatch, task, mine) : null,
  );
}

function taskFormView(model, dispatch, task, f) {
  const zone = displayZone(model);
  const submit = (e) => {
    e.preventDefault();
    dispatch("task.submit");
  };
  if (f.mode === "doneAt") {
    return h(
      "form",
      { class: "task-form", onsubmit: submit, "aria-label": `Done at a time: ${task.title}` },
      field(model, {
        id: `done-at-${task.id}`,
        label: `Done at (time in ${zone})`,
        type: "datetime-local",
        step: "1",
        value: f.at,
        required: true,
        mutates: true,
        autofocus: true,
        oninput: (e) => dispatch("task.field", "at", e.target.value),
      }),
      f.problem ? h("p", { class: "problem", role: "alert" }, f.problem) : null,
      h("div", { class: "actions" }, button(model, { mutates: true, type: "submit", class: "primary" }, "Complete"), button(model, { mutates: false, onclick: () => dispatch("task.cancel") }, "Cancel")),
    );
  }
  return h(
    "form",
    { class: "task-form", onsubmit: submit, "aria-label": `Skip: ${task.title}` },
    field(model, {
      id: `skip-reason-${task.id}`,
      label: "Reason for skipping",
      type: "text",
      value: f.reason,
      list: "recent-reasons",
      required: true,
      autocomplete: "off",
      mutates: true,
      autofocus: true,
      oninput: (e) => dispatch("task.field", "reason", e.target.value),
    }),
    f.problem ? h("p", { class: "problem", role: "alert" }, f.problem) : null,
    h(
      "div",
      { class: "actions" },
      button(model, { mutates: true, type: "submit", class: "primary", disabled: isBlank(f.reason) }, "Skip task"),
      button(model, { mutates: false, onclick: () => dispatch("task.cancel") }, "Cancel"),
    ),
  );
}
