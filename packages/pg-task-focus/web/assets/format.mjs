// Text for durations and relative times. Pure: no clock, no zone, no DOM.

function pad(n) {
  return String(n).padStart(2, "0");
}

/**
 * A timer's text: `MM:SS` (`H:MM:SS` from an hour up) while time remains, and
 * `+MM:SS` once it has run out, so overtime is a sign and not a colour.
 * @param {number} remainingSeconds negative in overtime
 */
export function timerText(remainingSeconds) {
  const overtime = remainingSeconds < 0;
  const total = Math.floor(Math.abs(overtime ? Math.ceil(remainingSeconds) : remainingSeconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  const body = h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${pad(m)}:${pad(s)}`;
  return overtime ? `+${body}` : body;
}

/**
 * A duration in words: `1h15m`, `5m`, `40s`; zero is `0s`.
 * @param {number} seconds non-negative
 */
export function durationWords(seconds) {
  const total = Math.max(0, Math.round(seconds));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return m > 0 ? `${h}h${pad(m)}m` : `${h}h`;
  if (m > 0) return `${m}m`;
  return `${s}s`;
}

/**
 * How far a due instant is from now, in words, and whether it has passed.
 * Never a negative time: a task past its due time is `overdue 12m`.
 * @returns {{text: string, overdue: boolean}}
 */
export function relativeDue(dueMs, nowMs) {
  const diff = Math.round((dueMs - nowMs) / 1000);
  if (diff >= 0) {
    return { text: diff < 60 ? "in under a minute" : `in ${durationWords(Math.floor(diff / 60) * 60)}`, overdue: false };
  }
  const late = -diff;
  return { text: late < 60 ? "overdue under a minute" : `overdue ${durationWords(Math.floor(late / 60) * 60)}`, overdue: true };
}

/** The first word of a title, which is what fits in a browser tab ("Deep work cycle" is "Deep"). */
export function shortTitle(title) {
  const t = (title ?? "").trim();
  const i = t.search(/\s/);
  return i < 0 ? t : t.slice(0, i);
}

/** `1 task`, `2 tasks`. */
export function plural(n, one, many = `${one}s`) {
  return `${n} ${n === 1 ? one : many}`;
}

/** Whether a string is empty or only white space, which is what the daemon calls blank. */
export function isBlank(s) {
  return typeof s !== "string" || s.trim() === "";
}

/** A link target the page will render: http and https only, never a script or data URL. */
export function safeHref(link) {
  try {
    const u = new URL(link);
    return u.protocol === "https:" || u.protocol === "http:" ? u.href : null;
  } catch {
    return null;
  }
}
