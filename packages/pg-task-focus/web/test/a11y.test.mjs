// Accessibility basics that can be checked on the tree the page draws, and the
// rule that no control is made except through the one function that classifies
// it. This is not a screen reader: it proves each control has a name, each field
// a label, each id is unique, and the structure a reader relies on is there.

import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import { textOf } from "../assets/vdom.mjs";
import { viewApp } from "../assets/view-app.mjs";
import { cycle, dimmed, model, period, readOnlyStore, state, T0, iso } from "./fixtures.mjs";
import { page } from "./page.mjs";

const assets = fileURLToPath(new URL("../assets/", import.meta.url));

test("no view makes a control except through control(), which makes it say whether it mutates", () => {
  for (const f of readdirSync(assets).filter((n) => n.endsWith(".mjs"))) {
    if (f === "view-common.mjs") continue;
    const src = readFileSync(assets + f, "utf8");
    assert.equal(/\bh\(\s*"(button|input|select|textarea)"/.test(src), false, `${f} builds a control by hand`);
  }
});

const items = [
  { id: "01JE1", event: { v: 1, id: "01JE1", at: iso(T0), effective_at: iso(T0), type: "cycle.started", data: { cycle_id: "A", type: "deep-work", title: "Deep work cycle", planned_minutes: 50 } }, corrected_by: ["01JE9"], retracted: false, original: { v: 1, id: "01JE1", at: iso(T0), effective_at: iso(T0), type: "cycle.started", data: { cycle_id: "A", type: "review", title: "Review cycle", planned_minutes: 50 } } },
  { id: "01JE2", event: { v: 1, id: "01JE2", at: iso(T0), effective_at: iso(T0), type: "task.skipped", data: { task_id: "t", reason: "r", batch: "01JB" } }, corrected_by: [], retracted: false },
];

const scenes = {
  today: () => model({ state: state({ focus: cycle(), dimmed: [dimmed()], resume_offer: { cycle: dimmed(), action: "switch" }, periods: [period("day", { ended: true, banner: "New day: roll over", today: "2026-10-08" }), period("week"), period("sprint")] }), undo: { seq: 1, label: "Completed x", ref: { event: "e" }, expiresAt: T0 + 5000 }, ui: { taskForm: { taskId: "day:2026-10-07:post-plan", mode: "skip", at: "", reason: "", problem: "" } } }),
  readOnly: () => model({ state: state({ store: readOnlyStore, focus: cycle() }) }),
  empty: () => model({ state: state({ initialized: false, periods: [], tasks: [] }) }),
  editor: () => model({ route: { name: "events" }, state: state({ focus: cycle() }), ui: { editor: { view: "corrected", from: "2026-10-05", to: "", type: "", items, loading: false, error: null, showOriginal: { "01JE1": true }, correct: { id: "01JE1", values: { effective_at: "2026-10-07T09:00:00", type: "deep-work", planned_minutes: "50" }, reason: "", problem: "", error: null }, rowError: null, op: { kind: "break", cycleId: "A", from: "", to: "", at: "", problem: "", error: null }, cycleItems: items, missing: "" } } }),
  periodModal: () => {
    const m = model({ state: state({ periods: [period("day", { ended: true, banner: "New day: roll over" }), period("week"), period("sprint")], focus: cycle() }) });
    m.ui.modal = "period";
    m.ui.period = { form: { tz: "America/New_York", profile: "", backdate: "", kinds: { day: { on: true, start: "2026-10-08", end: "", label: "" }, week: { on: false, start: "2026-10-08", end: "2026-10-14", label: "" }, sprint: { on: false, start: "2026-10-08", end: "2026-10-21", label: "" } } }, preview: null, previewing: false, problems: [], error: null, stale: false, rollover: { mode: "missed", skipAllReason: "", perTask: {} }, endAt: {}, endAtProblem: {} };
    return m;
  },
};

for (const [name, make] of Object.entries(scenes)) {
  test(`accessibility basics: ${name}`, () => {
    const p = page(viewApp(make(), () => {}));
    const all = p.all();

    // Every id is unique, and every reference points at one.
    const ids = all.map((n) => n.props.id).filter(Boolean);
    assert.deepEqual(ids.filter((id, i) => ids.indexOf(id) !== i), [], "duplicate ids");
    for (const n of all) {
      for (const attr of ["aria-labelledby", "aria-describedby", "for", "list"]) {
        const ref = n.props[attr];
        if (!ref) continue;
        for (const id of String(ref).split(/\s+/)) assert.ok(ids.includes(id), `${n.tag}[${attr}=${id}] points at nothing`);
      }
    }

    // Every button has a name.
    for (const b of all.filter((n) => n.tag === "button")) assert.ok((b.props["aria-label"] ?? textOf(b)).trim() !== "", "a button has no name");

    // Every field has a label: a label that names it, a label that wraps it, or its own aria-label.
    const labelled = new Set(all.filter((n) => n.tag === "label" && n.props.for).map((n) => n.props.for));
    const wrapped = new Set();
    for (const l of all.filter((n) => n.tag === "label")) for (const c of page(l).all()) if (["input", "select", "textarea"].includes(c.tag)) wrapped.add(c);
    for (const f of all.filter((n) => ["input", "select", "textarea"].includes(n.tag))) {
      assert.ok(labelled.has(f.props.id) || wrapped.has(f) || f.props["aria-label"], `a ${f.tag} (${f.props.type ?? ""} ${f.props.id ?? ""}) has no label`);
    }

    // Structure: one h1, no skipped heading level, regions are named.
    const heads = all.filter((n) => /^h[1-6]$/.test(n.tag)).map((n) => Number(n.tag[1]));
    assert.equal(heads.filter((x) => x === 1).length, 1);
    for (let i = 1; i < heads.length; i++) assert.ok(heads[i] - heads[i - 1] <= 1, `heading levels jump from h${heads[i - 1]} to h${heads[i]}`);
    for (const n of all.filter((x) => ["aside", "nav", "section"].includes(x.tag) && x.props["aria-labelledby"] === undefined)) {
      assert.ok(n.props["aria-label"], `a ${n.tag} has no name`);
    }
    for (const t of all.filter((n) => n.tag === "table")) {
      assert.ok(t.children.some((c) => c.tag === "caption"), "a table has no caption");
      assert.ok(page(t).where((n) => n.tag === "th").every((th) => th.props.scope === "col"), "a header cell has no scope");
    }

    // Live regions exist from the start, so what is put in them is announced.
    assert.equal(p.where((n) => n.props["aria-live"] === "polite").length, 1);
    assert.equal(p.where((n) => n.props["aria-live"] === "assertive").length, 1);
    // Nothing is a live region that changes every second.
    for (const n of p.where((x) => x.props.role === "timer")) assert.ok(!n.props["aria-live"] || n.props["aria-live"] === "off");
  });
}

test("overdue, overtime, paused and read-only are words and glyphs, not colour", () => {
  const p = page(viewApp(scenes.today(), () => {}));
  assert.ok(p.has("Paused") && p.has("Running"));
  const over = page(viewApp(model({ state: state({ focus: cycle({ remaining_seconds: -5 }) }) }), () => {}));
  assert.ok(over.has("Overtime"));
  const late = page(viewApp(model({ state: state({ tasks: [state().tasks[0]].map((t) => ({ ...t, due: "2026-10-07T12:00:00.000Z", overdue: true })) }) }), () => {}));
  assert.ok(late.has("Open, overdue") && late.has("overdue 1h"));
  assert.ok(page(viewApp(scenes.readOnly(), () => {})).has("READ-ONLY:"));
});

test("the stylesheet respects reduced motion and the colour scheme, and never hides focus", () => {
  const css = readFileSync(assets + "style.css", "utf8");
  assert.match(css, /prefers-reduced-motion:\s*reduce/);
  assert.match(css, /prefers-color-scheme:\s*dark/);
  assert.match(css, /:focus-visible/);
  assert.equal(/outline:\s*(none|0)\b/.test(css), false, "no rule removes the focus outline");
});
