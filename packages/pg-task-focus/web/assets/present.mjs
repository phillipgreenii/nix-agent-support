// The sentences and short texts the views share: a task's status, its due
// time, a cycle's status. Pure.

import { durationWords, relativeDue } from "./format.mjs";
import { parseInstant, stampInZone } from "./zone.mjs";

/**
 * A sentence the service sent twice: written out for the command line (each
 * instant in UTC and the rule zone, on a 24-hour clock) and as `parts`, pieces
 * of text and instants. The page rebuilds the sentence from the pieces, showing
 * each instant in the zone given (the browser's) in the page's compact form, so
 * the sentence has no zone name and no 24-hour clock. Without parts the
 * sentence is shown as sent.
 *
 * @param {string | undefined} sentence
 * @param {{text?: string, at?: string}[] | null | undefined} parts
 * @param {number} nowMs
 * @param {string} zone
 */
export function sentenceInZone(sentence, parts, nowMs, zone) {
  if (!Array.isArray(parts) || parts.length === 0) return sentence;
  return parts
    .map((p) => {
      if (p.at === undefined) return p.text ?? "";
      const ms = parseInstant(p.at);
      return ms === null ? p.at : stampInZone(ms, zone, nowMs);
    })
    .join("");
}

/**
 * The sentence of a no-op's notice: the service's `note` and its `note_parts`.
 *
 * @param {{note?: string, note_parts?: {text?: string, at?: string}[]}} result
 * @param {number} nowMs
 * @param {string} zone
 */
export function noteText(result, nowMs, zone) {
  return sentenceInZone(result.note, result.note_parts, nowMs, zone);
}

/** The due time in the zone given (the browser's), in the page's compact form: `9:30 AM`, `Mon 9:30 AM`, `Oct 12, 9:30 AM`. */
export function dueText(task, nowMs, zone) {
  const due = parseInstant(task.due);
  return due === null ? task.due : stampInZone(due, zone, nowMs);
}

/** The task's status in words (never a colour alone). */
export function statusText(task, nowMs, zone) {
  switch (task.status) {
    case "completed": {
      const at = parseInstant(task.resolved_at ?? "");
      return at === null ? "Done" : `Done ${stampInZone(at, zone, nowMs)}`;
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

/** `Next: <title>, due <time>, in 22m` (or `overdue 12m`). */
export function nextLine(task, nowMs, zone) {
  const rel = relativeDue(parseInstant(task.due) ?? nowMs, nowMs);
  return `Next: ${task.title}, due ${dueText(task, nowMs, zone)}, ${rel.text}`;
}

/** `Ran 12m of 25m` for a paused cycle. */
export function ranText(cycle) {
  return `Ran ${durationWords(cycle.elapsed_seconds)} of ${durationWords((cycle.planned_minutes + cycle.boost_minutes) * 60)}`;
}
