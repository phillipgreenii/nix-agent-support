import assert from "node:assert/strict";
import { test } from "node:test";
import { h, render, textOf, walk } from "../assets/vdom.mjs";
import { newRoot } from "./fakedom.mjs";

const html = (el) =>
  el.childNodes
    .map((c) => (c.nodeType === 3 ? c.nodeValue : `<${c.tagName.toLowerCase()}${[...c.attrs].map(([k, v]) => ` ${k}="${v}"`).join("")}>${html(c)}</${c.tagName.toLowerCase()}>`))
    .join("");

test("renders elements, attributes and text", () => {
  const { root } = newRoot();
  render(root, h("p", { class: "a", "aria-label": "x" }, "hi ", 3, null, false, ["there"]));
  assert.equal(html(root), '<p class="a" aria-label="x">hi 3there</p>');
});

test("updates in place: the same element survives, only what changed is touched", () => {
  const { root, doc } = newRoot();
  render(root, h("div", {}, h("input", { value: "a", class: "x" }), h("span", {}, "one")));
  const moved = doc.moves;
  const input = root.byTag("input")[0];
  const span = root.byTag("span")[0];
  render(root, h("div", {}, h("input", { value: "b", class: "y" }), h("span", {}, "two")));
  assert.equal(root.byTag("input")[0], input);
  assert.equal(root.byTag("span")[0], span);
  assert.equal(input.value, "b");
  assert.equal(input.getAttribute("class"), "y");
  assert.equal(textOf(h("i", {}, "x")), "x");
  assert.equal(span.textContent, "two");
  assert.equal(doc.moves, moved, "an update moves no node");
});

test("a keyed list keeps each element with its key when the order changes", () => {
  const { root } = newRoot();
  const item = (k) => h("li", { key: k }, k);
  render(root, h("ul", {}, item("a"), item("b"), item("c")));
  const [a, b, c] = root.byTag("li");
  render(root, h("ul", {}, item("c"), item("a")));
  const now = root.byTag("li");
  assert.deepEqual(now.map((n) => n.textContent), ["c", "a"]);
  assert.equal(now[0], c);
  assert.equal(now[1], a);
  assert.equal(b.parentNode, null, "the removed key's element is detached");
});

test("a focused field stays focused and keeps what was typed when the page re-renders", () => {
  const { root, doc } = newRoot();
  const view = (v) => h("form", {}, h("input", { key: "reason", value: v }), h("button", { type: "button" }, "Go"));
  render(root, view(""));
  const input = root.byTag("input")[0];
  input.focus();
  input.value = "typed";
  render(root, view("typed"));
  assert.equal(doc.activeElement, input);
  assert.equal(input.value, "typed");
  render(root, view("typed"));
  assert.equal(doc.activeElement, input);
});

test("handlers are properties and are replaced or removed", () => {
  const { root } = newRoot();
  let n = 0;
  render(root, h("button", { onclick: () => n++ }, "x"));
  const b = root.byTag("button")[0];
  b.onclick();
  render(root, h("button", {}, "x"));
  assert.equal(b.onclick, null);
  assert.equal(n, 1);
  assert.throws(() => render(root, h("button", { onclick: "alert(1)" }, "x")), /must be a function/);
});

test("boolean props are properties and attributes of other kinds are removed with false", () => {
  const { root } = newRoot();
  render(root, h("button", { disabled: true, "aria-busy": "true", title: "t" }, "x"));
  const b = root.byTag("button")[0];
  assert.equal(b.disabled, true);
  render(root, h("button", { disabled: false, "aria-busy": false }, "x"));
  assert.equal(b.disabled, false);
  assert.equal(b.getAttribute("aria-busy"), null);
  assert.equal(b.getAttribute("title"), null);
});

test("inline styles are refused", () => {
  const { root } = newRoot();
  assert.throws(() => render(root, h("p", { style: "color: red" }, "x")), /content security policy/);
});

test("a dialog is opened as a modal after it is in the document, and closed again", () => {
  const { root } = newRoot();
  render(root, h("div", {}, h("dialog", { modal: true }, "hello")));
  const d = root.byTag("dialog")[0];
  assert.equal(d.open, true);
  assert.equal(d.modalOpens, 1);
  render(root, h("div", {}, h("dialog", { modal: true }, "hello again")));
  assert.equal(d.modalOpens, 1, "an open dialog is not reopened");
  render(root, h("div", {}, h("dialog", { modal: false }, "bye")));
  assert.equal(d.open, false);
});

test("defaultOpen and autofocus act once, on creation", () => {
  const { root, doc } = newRoot();
  render(root, h("div", {}, h("details", { defaultOpen: true }, "d"), h("input", { autofocus: true })));
  const d = root.byTag("details")[0];
  const i = root.byTag("input")[0];
  assert.equal(d.open, true);
  assert.equal(doc.activeElement, i);
  d.open = false;
  doc.activeElement = null;
  render(root, h("div", {}, h("details", { defaultOpen: true }, "d"), h("input", { autofocus: true })));
  assert.equal(d.open, false, "the user's choice is not overridden");
  assert.equal(doc.activeElement, null);
});

test("walk and textOf read a tree", () => {
  const tree = h("div", {}, h("p", {}, "a ", h("b", {}, "b")), h("p", {}, "c"));
  assert.equal(textOf(tree), "a b c");
  assert.equal([...walk(tree)].filter((n) => n.tag === "p").length, 2);
});
