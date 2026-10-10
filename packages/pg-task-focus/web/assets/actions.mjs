// The requests the page makes, as plain data: a path, a body without its id
// (the store adds the idempotency id), a label for messages, and, for the
// three actions that offer an Undo, what the Undo says. Every cycle action
// names its cycle: the page never lets the daemon guess.

import { toRFC3339 } from "./zone.mjs";
import { isBlank } from "./format.mjs";

const enc = encodeURIComponent;

function withAt(body, atMs) {
  return atMs === undefined || atMs === null ? body : { ...body, effective_at: toRFC3339(atMs) };
}

/** Complete a task now, or at an instant the operator chose. */
export function completeTask(task, atMs) {
  return {
    path: `/api/v1/tasks/${enc(task.id)}/complete`,
    body: withAt({}, atMs),
    label: `Complete ${task.title}`,
    undo: { label: `Completed ${task.title}` },
  };
}

/** Skip a task for a reason; the reason is what the operator typed, without its outer white space. */
export function skipTask(task, reason, atMs) {
  return {
    path: `/api/v1/tasks/${enc(task.id)}/skip`,
    body: withAt({ reason: reason.trim() }, atMs),
    label: `Skip ${task.title}`,
    undo: { label: `Skipped ${task.title}` },
  };
}

/** Start a cycle of a type; a cycle that runs is interrupted by it, not refused. */
export function startCycle(type, minutes) {
  const body = { type };
  if (minutes) body.minutes = minutes;
  return { path: "/api/v1/cycles/start", body, label: `Start ${type}` };
}

export function pauseCycle(cycle) {
  return { path: "/api/v1/cycles/pause", body: { cycle_id: cycle.id }, label: `Pause ${cycle.title}` };
}

export function resumeCycle(cycle) {
  return { path: "/api/v1/cycles/resume", body: { cycle_id: cycle.id }, label: `Resume ${cycle.title}` };
}

/** Stop a cycle now, or at an earlier instant ("End at"). */
export function stopCycle(cycle, atMs) {
  return {
    path: "/api/v1/cycles/stop",
    body: withAt({ cycle_id: cycle.id }, atMs),
    label: `Stop ${cycle.title}`,
    undo: { label: `Stopped ${cycle.title}` },
  };
}

/** The same as stopCycle for a cycle known only by id, from the editor. */
export function endCycleAt(cycleId, title, atMs) {
  return {
    path: "/api/v1/cycles/stop",
    body: withAt({ cycle_id: cycleId }, atMs),
    label: `End ${title} at an earlier time`,
  };
}

export function boostCycle(cycle, minutes) {
  return { path: "/api/v1/cycles/boost", body: { cycle_id: cycle.id, minutes }, label: `Boost ${cycle.title}` };
}

/** Make a paused cycle the focus; the running cycle is paused in the same batch. */
export function switchTo(cycle) {
  return { path: "/api/v1/cycles/switch", body: { to: cycle.id }, label: `Switch to ${cycle.title}` };
}

/**
 * Save a cycle's whole form. A pair with an empty key or an empty value is
 * not sent, and an empty note is left out.
 */
export function annotateCycle(cycle, note, kv) {
  const body = { cycle_id: cycle.id };
  if (!isBlank(note)) body.note = note;
  body.kv = kv.filter((p) => !isBlank(p.key) && !isBlank(p.value)).map((p) => ({ key: p.key.trim(), value: p.value }));
  return { path: "/api/v1/cycles/annotate", body, label: `Save the note of ${cycle.title}` };
}

/** Back-fill a break a cycle forgot. */
export function breakCycle(cycleId, title, fromMs, toMs) {
  return {
    path: "/api/v1/cycles/break",
    body: { cycle_id: cycleId, from: toRFC3339(fromMs), to: toRFC3339(toMs) },
    label: `Insert a break in ${title}`,
  };
}

/** Correct an event; `fields` holds only the replacements. */
export function correctEvent(eventId, fields, reason) {
  const body = { fields };
  if (!isBlank(reason)) body.reason = reason.trim();
  return { path: `/api/v1/events/${enc(eventId)}/correct`, body, label: "Correct the event" };
}

/** Retract one event, or the whole batch it is a member of. */
export function retract(item, batchId, reason, describe) {
  const body = isBlank(reason) ? {} : { reason: reason.trim() };
  return batchId
    ? { path: `/api/v1/batches/${enc(batchId)}/retract`, body, label: `Retract the batch`, undo: { label: `Retracted: ${describe}` } }
    : { path: `/api/v1/events/${enc(item.id)}/retract`, body, label: "Retract the event", undo: { label: `Retracted: ${describe}` } };
}

/** A period change; its preview is the same request through the store's preview. */
export function changePeriods(request) {
  return { path: "/api/v1/periods/change", body: { ...request }, label: "Change the periods", undo: { label: "Changed the periods" } };
}

/** A profile change. */
export function changeProfile(profile, expectedVersion) {
  const body = { profile };
  if (expectedVersion) body.expected_version = expectedVersion;
  return { path: "/api/v1/profile/change", body, label: "Change the profile", undo: { label: `Changed the profile to ${profile}` } };
}
