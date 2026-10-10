// What a deep link shows when its task or cycle is not in the current state
// (a task of an earlier period, a stopped cycle): the log's account of it. A
// task or cycle that is in the state is shown where the page already shows it,
// marked and focused; an id the log does not know is said in words.

import { entityItems, historyOf, routeKey } from "./feature-history.mjs";
import { effectiveMs, lookupOf, sentence } from "./events.mjs";
import { durationWords } from "./format.mjs";
import { cycleOfState, displayZone, taskOfState } from "./select.mjs";
import { cycleSegments, runningMs } from "./timeline.mjs";
import { h } from "./vdom.mjs";
import { dateTimeInZone } from "./zone.mjs";

/** The detail card for the route, or null when the route names nothing the state does not already show. */
export function entityDetail(model) {
  const route = model.route;
  if (route.name !== "task" && route.name !== "cycle") return null;
  if (!model.state) return null;
  const inState = route.name === "task" ? taskOfState(model.state, route.id) : cycleOfState(model.state, route.id);
  if (inState) return null;
  const hist = historyOf(model);
  const label = route.name === "task" ? "task" : "cycle";
  if (!hist || hist.key !== routeKey(route) || hist.loading) {
    return h("section", { class: "detail", "aria-live": "polite" }, h("p", { class: "empty" }, `Reading the log for this ${label}…`));
  }
  if (hist.error) {
    return h("section", { class: "detail" }, h("p", { class: "problem", role: "alert" }, hist.error.detail ?? `The log could not be read for this ${label}.`));
  }
  const zone = displayZone(model);
  const items = entityItems(hist.items, route);
  if (items.length === 0) {
    return h("section", { class: "detail", "data-detail": "unknown" }, h("p", { class: "problem", role: "alert" }, `The log has no ${label} with the id ${route.id}.`), h("p", {}, "The rest of the page is unchanged."));
  }
  const look = lookupOf(hist.items, model.state);
  const live = items.filter((it) => !it.retracted);
  const lines = live.map((it) => h("li", { key: it.id }, `${dateTimeInZone(effectiveMs(it), zone)} ${zone}: `, sentence(it, look, zone), " ", h("a", { href: `#/events/${encodeURIComponent(it.id)}` }, "In the editor")));
  return route.name === "task" ? taskDetail(route, live, lines, look) : cycleDetail(model, route, hist, lines, zone);
}

const RESOLUTIONS = ["task.completed", "task.skipped", "task.missed", "task.withdrawn", "task.reinstated"];

function taskDetail(route, live, lines, look) {
  const last = [...live].filter((it) => RESOLUTIONS.includes(it.event.type)).sort((a, b) => effectiveMs(a) - effectiveMs(b)).at(-1);
  const status = !last ? "open" : { "task.completed": "done", "task.skipped": "skipped", "task.missed": "missed", "task.withdrawn": "withdrawn", "task.reinstated": "open (reinstated)" }[last.event.type];
  return h(
    "section",
    { class: "detail", "data-detail": "task", "aria-labelledby": "detail-h", tabindex: "-1" },
    h("h2", { id: "detail-h" }, look.task(route.id)),
    h("p", {}, `This task is not in the current periods. Its last known state in the log: ${status}.`),
    h("ul", {}, lines),
  );
}

function cycleDetail(model, route, hist, lines, zone) {
  const rec = cycleSegments(hist.items, route.id);
  const annotated = hist.items.filter((it) => !it.retracted && it.event.type === "cycle.annotated" && it.event.data.cycle_id === route.id).at(-1);
  const note = annotated?.event.data;
  return h(
    "section",
    { class: "detail", "data-detail": "cycle", "aria-labelledby": "detail-h", tabindex: "-1" },
    h("h2", { id: "detail-h" }, rec.title || route.id),
    h("p", {}, rec.stoppedAt !== null ? `Stopped at ${dateTimeInZone(rec.stoppedAt, zone)} ${zone}. ` : "Not running now. ", `Running time ${durationWords(runningMs(rec.segments, model.now) / 1000)}.`),
    note?.note ? h("p", { class: "note" }, `Note: ${note.note}`) : null,
    note?.kv?.length > 0 ? h("ul", { class: "kv" }, note.kv.map((p, i) => h("li", { key: i }, `${p.key}: ${p.value}`))) : null,
    h("ul", {}, lines),
  );
}
