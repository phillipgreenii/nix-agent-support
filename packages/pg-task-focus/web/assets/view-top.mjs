// The top of the page: the banners, the header and the navigation. The
// READ-ONLY banner comes first and is an alert, so it is announced when it
// appears and is visible without scrolling in every area.

import { relativeDue } from "./format.mjs";
import { nextLine } from "./present.mjs";
import { KINDS, KIND_TITLES, periodOf, readOnlySentence } from "./select.mjs";
import { h } from "./vdom.mjs";
import { button, errorBox } from "./view-common.mjs";
import { clockInZone, parseInstant } from "./zone.mjs";

/** The banners: read-only, connection, a quiet notice, and the last failed request. */
export function banners(model, dispatch) {
  const sentence = readOnlySentence(model.state?.store);
  const lost = model.conn === "lost" || (model.loadError && !model.loaded);
  return h(
    "div",
    { class: "banners", id: "banners" },
    sentence ? h("p", { class: "banner banner-readonly", role: "alert", "data-banner": "read-only" }, sentence) : null,
    lost ? connectionBanner(model, dispatch) : null,
    model.notice ? h("p", { class: "banner banner-notice", role: "status", key: `n${model.notice.seq}` }, model.notice.text, button(model, { mutates: false, onclick: () => dispatch("notice.dismiss") }, "Dismiss")) : null,
    model.error
      ? errorBox(model, model.error, {
          onRetry: () => dispatch("error.retry"),
          onChoose: (id) => dispatch("error.choose", id),
          onDismiss: () => dispatch("error.dismiss"),
        })
      : null,
  );
}

function connectionBanner(model, dispatch) {
  const e = model.loadError;
  let text;
  if (!model.loaded) {
    text = e ? e.detail : "Reading the state from the service…";
  } else {
    const when = clockInZone(model.receivedAt, model.browserZone || "UTC");
    text = `The connection to the service was lost. This is the state it had at ${when}${model.browserZone ? ` ${model.browserZone}` : " UTC"}; it will update when the service is back.`;
  }
  return h(
    "p",
    { class: "banner banner-connection", role: "status", "data-banner": "connection" },
    text,
    " ",
    button(model, { mutates: false, onclick: () => dispatch("refresh") }, "Try now"),
  );
}

/** The header: the periods, the profile, what is next, and the change controls. */
export function header(model, dispatch) {
  const st = model.state;
  if (!st) return h("header", { class: "page-header" }, h("h1", {}, "pg-task-focus"));
  return h(
    "header",
    { class: "page-header" },
    h("h1", {}, "pg-task-focus"),
    st.initialized
      ? [
          h(
            "ul",
            { class: "periods", "aria-label": "Current periods" },
            KINDS.map((kind) => periodItem(model, dispatch, periodOf(st, kind), kind)),
          ),
          h(
            "p",
            { class: "profile" },
            "Profile: ",
            h("strong", {}, st.profile),
            " ",
            button(model, { mutates: true, onclick: () => dispatch("profile.open") }, "Change profile"),
          ),
          nextItem(model, st),
          h("p", {}, button(model, { mutates: true, onclick: () => dispatch("period.open"), id: "change-periods" }, "Change periods")),
        ]
      : h(
          "div",
          { class: "setup" },
          h("p", {}, "Set your day, week and sprint to begin."),
          button(model, { mutates: true, onclick: () => dispatch("period.open"), id: "change-periods" }, "Set up"),
        ),
  );
}

function periodItem(model, dispatch, p, kind) {
  if (!p) return h("li", { key: kind, class: "period period-none" }, h("strong", {}, KIND_TITLES[kind]), ": none yet");
  const dates = p.kind === "day" ? p.start : `${p.start} to ${p.end}`;
  return h(
    "li",
    { key: kind, class: p.ended ? "period period-ended" : "period" },
    h("strong", {}, KIND_TITLES[kind]),
    `: ${dates}${p.label ? ` (${p.label})` : ""}, ${p.zone}`,
    p.ended
      ? [" ", button(model, { mutates: true, class: "roll", onclick: () => dispatch("period.open") }, p.banner), h("span", { class: "sr-only" }, ` ${KIND_TITLES[kind]} ended`)]
      : null,
  );
}

function nextItem(model, st) {
  if (!st.next) return h("p", { class: "next", "data-next": "none" }, "Nothing is due.");
  const overdue = relativeDue(parseInstant(st.next.due) ?? model.now, model.now).overdue;
  return h("p", { class: overdue ? "next next-overdue" : "next", "data-next": st.next.id }, overdue ? h("span", { "aria-hidden": "true" }, "⚠ ") : null, nextLine(st.next, model.now));
}

/** The navigation between the two areas. */
export function nav(model) {
  const here = model.route.name === "events" ? "events" : "today";
  return h(
    "nav",
    { class: "areas", "aria-label": "Areas" },
    h("a", { href: "#/", "aria-current": here === "today" ? "page" : undefined }, "Today"),
    h("a", { href: "#/events", "aria-current": here === "events" ? "page" : undefined }, "Editor"),
  );
}
