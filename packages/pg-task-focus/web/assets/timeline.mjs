// A cycle's running segments, derived from its events, and what a proposed
// fix would do to them. This is the before-and-after timeline of the editor's
// "Insert break" and "End at". It is arithmetic on segments, not a copy of the
// daemon's rules: the daemon checks the change by replaying the log when the
// operator confirms, and its refusal is the authority.

import { effectiveMs } from "./events.mjs";

/**
 * @typedef {{start: number, end: number | null}} Segment  end null is a segment still open
 */

/**
 * The running segments of a cycle from the corrected events of the log.
 *
 * Another cycle's start that interrupts this one pauses it at that instant
 * (the daemon derives that pause; it is not an event of this cycle).
 *
 * @param {object[]} items event items, in log order
 * @param {string} cycleId
 * @returns {{found: boolean, title: string, type: string, startedAt: number | null, stoppedAt: number | null, segments: Segment[]}}
 */
export function cycleSegments(items, cycleId) {
  const mine = [];
  items.forEach((it, i) => {
    if (it.retracted) return;
    const e = it.event;
    const d = e.data ?? {};
    const relevant =
      (d.cycle_id === cycleId && ["cycle.started", "cycle.paused", "cycle.resumed", "cycle.stopped"].includes(e.type)) ||
      (e.type === "cycle.started" && d.interrupts === cycleId);
    if (relevant) mine.push({ at: effectiveMs(it), i, e });
  });
  mine.sort((a, b) => a.at - b.at || a.i - b.i);

  const out = { found: false, title: "", type: "", startedAt: null, stoppedAt: null, segments: [] };
  let open = null;
  const close = (at) => {
    if (open !== null) {
      out.segments.push({ start: open, end: at });
      open = null;
    }
  };
  for (const { at, e } of mine) {
    const d = e.data;
    switch (e.type) {
      case "cycle.started":
        if (d.cycle_id === cycleId) {
          out.found = true;
          out.title = d.title;
          out.type = d.type;
          out.startedAt = at;
          open = at;
        } else {
          close(at);
        }
        break;
      case "cycle.paused":
        close(at);
        break;
      case "cycle.resumed":
        if (open === null && out.stoppedAt === null) open = at;
        break;
      case "cycle.stopped":
        close(at);
        out.stoppedAt = at;
        break;
      default:
    }
  }
  if (open !== null) out.segments.push({ start: open, end: null });
  return out;
}

/** The segments with the interval [from, to) carved out of them: a break back-filled. */
export function withBreak(segments, from, to) {
  const out = [];
  for (const s of segments) {
    const end = s.end === null ? Infinity : s.end;
    if (to <= s.start || from >= end) {
      out.push({ ...s });
      continue;
    }
    if (from > s.start) out.push({ start: s.start, end: from });
    if (to < end) out.push({ start: to, end: s.end });
  }
  return out;
}

/** The segments clipped to end at an instant: a cycle ended at an earlier time. */
export function withEndAt(segments, at) {
  const out = [];
  for (const s of segments) {
    if (s.start >= at) continue;
    out.push({ start: s.start, end: s.end === null || s.end > at ? at : s.end });
  }
  return out;
}

/** The running time of segments in milliseconds; an open segment runs to `now`. */
export function runningMs(segments, now) {
  let total = 0;
  for (const s of segments) total += Math.max(0, (s.end ?? now) - s.start);
  return total;
}

/**
 * The before and after of a proposed fix.
 * @param {ReturnType<typeof cycleSegments>} rec
 * @param {{kind: "break", from: number, to: number} | {kind: "endat", at: number}} change
 * @param {number} now
 */
export function previewChange(rec, change, now) {
  const before = rec.segments;
  const after = change.kind === "break" ? withBreak(before, change.from, change.to) : withEndAt(before, change.at);
  return {
    before: { segments: before, ms: runningMs(before, now) },
    after: { segments: after, ms: runningMs(after, now) },
    // When "End at" names an instant after the existing stop, nothing changes.
    unchanged: JSON.stringify(before) === JSON.stringify(after),
  };
}
