// Civil dates and times in a named IANA zone, built on Intl (which carries the
// zone database), with the design's two rules for the awkward cases: a civil
// time that does not exist resolves to the first valid instant after the gap,
// and one that occurs twice resolves to its earlier occurrence. Nothing here
// reads the clock or the host's zone; every function names the zone it uses.

const formatters = new Map();

function formatter(tz) {
  let f = formatters.get(tz);
  if (!f) {
    f = new Intl.DateTimeFormat("en-US", {
      timeZone: tz,
      hourCycle: "h23",
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
    });
    formatters.set(tz, f);
  }
  return f;
}

/** Whether Intl knows the zone name. */
export function isZone(tz) {
  if (typeof tz !== "string" || tz === "") return false;
  try {
    formatter(tz);
    return true;
  } catch {
    return false;
  }
}

/**
 * The civil fields of an instant in a zone.
 * @param {number} ms
 * @param {string} tz
 * @returns {{year:number, month:number, day:number, hour:number, minute:number, second:number}}
 */
export function partsInZone(ms, tz) {
  const out = {};
  for (const p of formatter(tz).formatToParts(new Date(ms))) {
    if (p.type !== "literal") out[p.type] = Number(p.value);
  }
  return out;
}

/** The zone's offset from UTC at an instant, in milliseconds (east is positive). */
export function offsetMs(ms, tz) {
  const p = partsInZone(ms, tz);
  const asUTC = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second);
  return asUTC - Math.floor(ms / 1000) * 1000;
}

const DAY = 86400000;

/**
 * The instant of a civil date and time in a zone.
 *
 * A time that does not exist (the clocks skipped it) is the first valid instant
 * after the gap, and one that occurs twice is its earlier occurrence.
 *
 * @param {{year:number, month:number, day:number, hour:number, minute:number, second?:number}} c
 * @param {string} tz
 * @returns {number} milliseconds since the epoch
 */
export function civilToInstant(c, tz) {
  const asUTC = Date.UTC(c.year, c.month - 1, c.day, c.hour, c.minute, c.second ?? 0);
  const before = offsetMs(asUTC - DAY, tz);
  const after = offsetMs(asUTC + DAY, tz);
  const valid = [];
  for (const o of new Set([before, after])) {
    const t = asUTC - o;
    if (offsetMs(t, tz) === o) valid.push(t);
  }
  if (valid.length > 0) return Math.min(...valid);
  // In a gap: the answer is the instant the offset changed. It lies between the
  // two candidates, which sit on either side of it, so bisect to the second.
  let lo = Math.min(asUTC - before, asUTC - after);
  let hi = Math.max(asUTC - before, asUTC - after);
  // lo has the offset `before` (the earlier one); find the first instant that has `after`.
  while (hi - lo > 1000) {
    const mid = lo + Math.max(1000, Math.floor((hi - lo) / 2000) * 1000);
    if (offsetMs(mid, tz) === after) hi = mid;
    else lo = mid;
  }
  return hi;
}

function pad(n, width = 2) {
  return String(n).padStart(width, "0");
}

/** `YYYY-MM-DD` of an instant in a zone. */
export function dateInZone(ms, tz) {
  const p = partsInZone(ms, tz);
  return `${pad(p.year, 4)}-${pad(p.month)}-${pad(p.day)}`;
}

/** `HH:MM` of an instant in a zone. */
export function clockInZone(ms, tz) {
  const p = partsInZone(ms, tz);
  return `${pad(p.hour)}:${pad(p.minute)}`;
}

/** `YYYY-MM-DDTHH:MM`, the value of a datetime-local input, of an instant in a zone. */
export function inputValueInZone(ms, tz) {
  return `${dateInZone(ms, tz)}T${clockInZone(ms, tz)}`;
}

/** `YYYY-MM-DDTHH:MM:SS`, for a datetime-local input that keeps seconds (a correction must not lose them). */
export function inputValueSecondsInZone(ms, tz) {
  const p = partsInZone(ms, tz);
  return `${inputValueInZone(ms, tz)}:${pad(p.second)}`;
}

/** `YYYY-MM-DD HH:MM` of an instant in a zone. */
export function dateTimeInZone(ms, tz) {
  return `${dateInZone(ms, tz)} ${clockInZone(ms, tz)}`;
}

const INPUT_RE = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2}))?$/;

/**
 * Reads the value of a datetime-local input as a civil time in a zone.
 * @returns {number | null} the instant, or null when the text is not a date and time
 */
export function instantFromInput(value, tz) {
  const m = INPUT_RE.exec((value ?? "").trim());
  if (!m) return null;
  const c = { year: +m[1], month: +m[2], day: +m[3], hour: +m[4], minute: +m[5], second: m[6] ? +m[6] : 0 };
  if (c.month < 1 || c.month > 12 || c.day < 1 || c.day > 31 || c.hour > 23 || c.minute > 59 || c.second > 59) return null;
  // Date.UTC rolls 31 February into March; a date that does not exist is not input.
  const probe = new Date(Date.UTC(c.year, c.month - 1, c.day));
  if (probe.getUTCMonth() !== c.month - 1 || probe.getUTCDate() !== c.day) return null;
  if (!isZone(tz)) return null;
  return civilToInstant(c, tz);
}

/** An instant as the daemon's request instant: RFC 3339 in UTC with milliseconds. */
export function toRFC3339(ms) {
  return new Date(ms).toISOString();
}

/** Parses an RFC 3339 instant from the daemon into milliseconds. */
export function parseInstant(s) {
  const ms = Date.parse(s);
  return Number.isNaN(ms) ? null : ms;
}

const DATE_RE = /^(\d{4})-(\d{2})-(\d{2})$/;

/** Adds days to a `YYYY-MM-DD` civil date. */
export function addDays(date, n) {
  const m = DATE_RE.exec(date);
  if (!m) return null;
  const t = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3] + n));
  return `${pad(t.getUTCFullYear(), 4)}-${pad(t.getUTCMonth() + 1)}-${pad(t.getUTCDate())}`;
}

/** The whole days from civil date a to civil date b (negative when b is earlier). */
export function daysBetween(a, b) {
  const x = DATE_RE.exec(a);
  const y = DATE_RE.exec(b);
  if (!x || !y) return null;
  return Math.round((Date.UTC(+y[1], +y[2] - 1, +y[3]) - Date.UTC(+x[1], +x[2] - 1, +x[3])) / DAY);
}

/** The zone the browser reports, or empty when it reports none. */
export function browserZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "";
  } catch {
    return "";
  }
}

/** The zone names the browser knows, for a list of suggestions; empty where it cannot say. */
export function knownZones() {
  try {
    return typeof Intl.supportedValuesOf === "function" ? Intl.supportedValuesOf("timeZone") : [];
  } catch {
    return [];
  }
}
