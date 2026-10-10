// What the editor knows about the daemon's events: how to say one in a
// sentence, which of its fields can be corrected, and how events index the
// tasks and cycles they name. An item is one entry of GET /api/v1/events:
// {id, event, original?, corrected_by, retracted, retracted_by?}.

import { clockInZone, dateInZone, parseInstant } from "./zone.mjs";

/** The event types that cannot be corrected (the daemon refuses them). */
const NOT_CORRECTABLE = new Set(["event.corrected", "event.retracted", "batch.committed"]);

/** Whether an event of this type can be corrected. */
export function isCorrectable(type) {
  return !NOT_CORRECTABLE.has(type);
}

/** Whether an event of this type can be retracted: every type but the batch terminator. */
export function isRetractable(type) {
  return type !== "batch.committed";
}

/** The last characters of an id, which is where a ULID differs. */
export function shortId(id) {
  return typeof id === "string" && id.length > 8 ? `…${id.slice(-8)}` : String(id ?? "");
}

/**
 * The batch an event is a member of, or "". An event that is a member of a
 * batch MUST be retracted only through the batch.
 */
export function batchOf(item) {
  const e = item.event;
  if (e.type === "event.retracted") return "";
  if (e.type === "batch.committed") return e.data.batch ?? "";
  return e.data?.batch ?? "";
}

/**
 * The editable fields of an event type besides the time, which every event has.
 * `kind` is how the form edits it.
 * @returns {{name: string, label: string, kind: "text"|"longtext"|"number"|"date"|"instant"|"cycle-type"|"kv"}[]}
 */
export function editableFields(type, data = {}) {
  switch (type) {
    case "period.changed":
      return [
        ...(data.end ? [{ name: "end", label: "Last day", kind: "date" }] : []),
        { name: "tz", label: "Zone", kind: "text" },
        { name: "label", label: "Label", kind: "text" },
      ];
    case "task.materialized":
      return [
        { name: "title", label: "Title", kind: "text" },
        { name: "group", label: "Group", kind: "text" },
        { name: "link", label: "Link", kind: "text" },
        { name: "due", label: "Due", kind: "instant" },
      ];
    case "task.skipped":
      return [{ name: "reason", label: "Reason", kind: "text" }];
    case "cycle.started":
      return [
        { name: "type", label: "Type", kind: "cycle-type" },
        { name: "planned_minutes", label: "Planned minutes", kind: "number" },
      ];
    case "cycle.boosted":
      return [{ name: "minutes", label: "Minutes added", kind: "number" }];
    case "cycle.annotated":
      return [
        { name: "note", label: "Note", kind: "longtext" },
        { name: "kv", label: "Pairs", kind: "kv" },
      ];
    default:
      return [];
  }
}

/** The ms of an item's effective instant. */
export function effectiveMs(item) {
  return parseInstant(item.event.effective_at) ?? 0;
}

/**
 * Indexes the titles of tasks and cycles the events (and the current state)
 * name, so a sentence can say "Post the plan" and not an id.
 * @returns {{task: (id: string) => string, cycle: (id: string) => string, event: (id: string) => object | undefined}}
 */
export function lookupOf(items, state) {
  const tasks = new Map();
  const cycles = new Map();
  const events = new Map();
  for (const t of state?.tasks ?? []) tasks.set(t.id, t.title);
  for (const c of [state?.focus, ...(state?.dimmed ?? [])]) if (c) cycles.set(c.id, c.title);
  for (const it of items ?? []) {
    events.set(it.id, it);
    const e = it.event;
    if (e.type === "task.materialized") tasks.set(e.data.task_id, e.data.title);
    if (e.type === "cycle.started") cycles.set(e.data.cycle_id, e.data.title);
  }
  return {
    // A task id is readable (day:2026-10-07:post-plan); a cycle id is a ULID, of which the tail tells them apart.
    task: (id) => tasks.get(id) ?? id,
    cycle: (id) => cycles.get(id) ?? shortId(id),
    event: (id) => events.get(id),
  };
}

/**
 * An event in one sentence, in the zone given.
 * @param {object} item
 * @param {ReturnType<typeof lookupOf>} look
 * @param {string} zone
 */
export function sentence(item, look, zone) {
  const e = item.event;
  const d = e.data ?? {};
  switch (e.type) {
    case "period.changed":
      return d.end
        ? `${cap(d.kind)} ${d.start} to ${d.end} (${d.tz})${d.label ? `: ${d.label}` : ""}`
        : `${cap(d.kind)} ${d.start} (${d.tz})${d.label ? `: ${d.label}` : ""}`;
    case "profile.changed":
      return `Profile set to ${d.profile}`;
    case "task.materialized": {
      const due = parseInstant(d.due);
      const tz = d.due_rule?.tz ?? zone;
      return `Task added: ${d.title}, due ${due === null ? d.due : `${dateInZone(due, tz)} ${clockInZone(due, tz)} ${tz}`}`;
    }
    case "task.completed":
      return `Completed: ${look.task(d.task_id)}`;
    case "task.skipped":
      return `Skipped: ${look.task(d.task_id)} (reason: ${d.reason})`;
    case "task.missed":
      return `Marked missed: ${look.task(d.task_id)}`;
    case "task.withdrawn":
      return `Withdrawn from the profile: ${look.task(d.task_id)}`;
    case "task.reinstated":
      return `Reinstated in the profile: ${look.task(d.task_id)}`;
    case "cycle.started":
      return `Started: ${d.title} (${d.type}), ${d.planned_minutes} min planned${d.interrupts ? `, interrupting ${look.cycle(d.interrupts)}` : ""}`;
    case "cycle.paused":
      return `Paused: ${look.cycle(d.cycle_id)}`;
    case "cycle.resumed":
      return `Resumed: ${look.cycle(d.cycle_id)}`;
    case "cycle.boosted":
      return `Boosted: ${look.cycle(d.cycle_id)} by ${d.minutes} min`;
    case "cycle.stopped":
      return `Stopped: ${look.cycle(d.cycle_id)}`;
    case "cycle.annotated": {
      const n = (d.kv ?? []).length;
      return `Annotated: ${look.cycle(d.cycle_id)}${d.note ? " (note" : " (no note"}, ${n} ${n === 1 ? "pair" : "pairs"})`;
    }
    case "event.corrected": {
      const t = look.event(d.target);
      return `Corrected ${t ? `"${t.event.type}"` : "an event"} ${shortId(d.target)}: ${Object.keys(d.fields ?? {}).join(", ")}${d.reason ? ` (${d.reason})` : ""}`;
    }
    case "event.retracted":
      return d.target_batch
        ? `Retracted batch ${shortId(d.target_batch)}${d.reason ? ` (${d.reason})` : ""}`
        : `Retracted ${look.event(d.target) ? `"${look.event(d.target).event.type}" ` : ""}${shortId(d.target)}${d.reason ? ` (${d.reason})` : ""}`;
    case "batch.committed":
      return `Batch ${shortId(d.batch)} committed`;
    default:
      return e.type;
  }
}

function cap(s) {
  return s ? s[0].toUpperCase() + s.slice(1) : "";
}

/** How many events of the list are members of a batch (the batch terminator included). */
export function batchSize(items, batch) {
  return items.filter((it) => batchOf(it) === batch).length;
}
