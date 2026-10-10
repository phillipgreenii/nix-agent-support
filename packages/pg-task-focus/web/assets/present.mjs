// The sentences and short texts the views share: a task's status, its due
// time with its zone, a cycle's status. Pure.

import { durationWords, relativeDue } from "./format.mjs";
import { clockInZone, dateInZone, parseInstant } from "./zone.mjs";

/** `09:30 America/New_York`, with the date when it is not today in that zone. */
export function dueText(task, nowMs) {
  const due = parseInstant(task.due);
  if (due === null) return `${task.due} ${task.due_zone}`;
  const tz = task.due_zone;
  const time = `${clockInZone(due, tz)} ${tz}`;
  return dateInZone(due, tz) === dateInZone(nowMs, tz) ? time : `${dateInZone(due, tz)} ${time}`;
}

/** The task's status in words (never a colour alone). */
export function statusText(task, nowMs) {
  switch (task.status) {
    case "completed": {
      const at = parseInstant(task.resolved_at ?? "");
      return at === null ? "Done" : `Done ${clockInZone(at, task.due_zone)} ${task.due_zone}`;
    }
    case "skipped":
      return "Skipped";
    case "missed":
      return "Missed";
    case "withdrawn":
      return "Withdrawn";
    default:
      return relativeDue(parseInstant(task.due) ?? nowMs, nowMs).overdue ? "Open, overdue" : "Open";
  }
}

/** The glyph of a status, which accompanies its words and is hidden from assistive technology. */
export function statusIcon(task) {
  return { completed: "✓", skipped: "⏭", missed: "✗", withdrawn: "–" }[task.status] ?? "○";
}

/** `Next: <title>, due <time zone>, in 22m` (or `overdue 12m`). */
export function nextLine(task, nowMs) {
  const rel = relativeDue(parseInstant(task.due) ?? nowMs, nowMs);
  return `Next: ${task.title}, due ${dueText(task, nowMs)}, ${rel.text}`;
}

/** `Ran 12m of 25m` for a paused cycle. */
export function ranText(cycle) {
  return `Ran ${durationWords(cycle.elapsed_seconds)} of ${durationWords((cycle.planned_minutes + cycle.boost_minutes) * 60)}`;
}
