// A tiny virtual tree and the patcher that applies it to the DOM.
//
// A view is a pure function from a model to a tree of plain objects built with
// h(). That is what lets a test assert on what the page says and which
// controls are enabled without a browser. render() then makes the real DOM
// match the tree: elements are reused (by key, else by position and tag) so a
// focused field, a scroll position and an open <details> survive an update.
//
// Props: `key` identifies an element among its siblings and is not applied.
// `on*` functions become event properties. `value`, `checked` and `selected`
// are set as properties, and only when they differ, so typing is not disturbed.
// Booleans become properties (`disabled`, `hidden`, `required`, `readOnly`).
// `defaultOpen` and `autofocus` act once, when the element is created. `modal`
// opens a <dialog> with showModal() and closes it. `style` is refused: the
// page's content security policy forbids inline styles, so a style is a class.

const TEXT = "#text";

/**
 * @param {string} tag
 * @param {Record<string, unknown> | null} [props]
 * @param {...unknown} children strings, numbers, vnodes, arrays of them; null, undefined and false are dropped
 */
export function h(tag, props, ...children) {
  const p = props ?? {};
  return { tag, props: p, children: normalize(children), key: p.key ?? null };
}

function normalize(list) {
  const out = [];
  const visit = (c) => {
    if (c === null || c === undefined || c === false || c === true) return;
    if (Array.isArray(c)) {
      for (const x of c) visit(x);
    } else if (typeof c === "string" || typeof c === "number") {
      out.push({ tag: TEXT, text: String(c), props: {}, children: [], key: null });
    } else {
      out.push(c);
    }
  };
  visit(list);
  return out;
}

const BOOLEAN_PROPS = new Map([
  ["disabled", "disabled"],
  ["hidden", "hidden"],
  ["required", "required"],
  ["readonly", "readOnly"],
  ["multiple", "multiple"],
]);
const VALUE_PROPS = new Set(["value", "checked", "selected"]);
const CREATE_ONLY = new Set(["defaultOpen", "autofocus"]);
const SKIPPED = new Set(["key", "modal"]);

function setAttr(el, name, val) {
  if (val === false || val === null || val === undefined) el.removeAttribute(name);
  else el.setAttribute(name, val === true ? "" : String(val));
}

function applyAttrs(el, oldProps, newProps, ctx, creating) {
  for (const name of Object.keys(oldProps)) {
    if (name in newProps || SKIPPED.has(name) || CREATE_ONLY.has(name) || VALUE_PROPS.has(name)) continue;
    if (name.startsWith("on")) el[name] = null;
    else if (BOOLEAN_PROPS.has(name)) el[BOOLEAN_PROPS.get(name)] = false;
    else el.removeAttribute(name);
  }
  for (const [name, val] of Object.entries(newProps)) {
    if (SKIPPED.has(name) || VALUE_PROPS.has(name)) continue;
    if (name === "style") throw new Error("inline styles are refused by the content security policy; use a class");
    if (CREATE_ONLY.has(name)) {
      if (creating && val) {
        if (name === "defaultOpen") el.open = true;
        else ctx.effects.push(() => el.focus());
      }
      continue;
    }
    if (name.startsWith("on")) {
      if (typeof val === "function" || val === null || val === undefined) el[name] = val ?? null;
      else throw new Error(`${name} must be a function`);
      continue;
    }
    if (BOOLEAN_PROPS.has(name)) {
      const prop = BOOLEAN_PROPS.get(name);
      const want = !!val;
      if (el[prop] !== want) el[prop] = want;
      continue;
    }
    if (oldProps[name] === val && !creating) continue;
    setAttr(el, name, val);
  }
}

function applyValues(el, newProps) {
  for (const name of VALUE_PROPS) {
    if (!(name in newProps)) continue;
    const val = name === "value" ? String(newProps.value ?? "") : !!newProps[name];
    if (el[name] !== val) el[name] = val;
  }
}

function createNode(n, ctx) {
  if (n.tag === TEXT) return ctx.doc.createTextNode(n.text);
  const el = ctx.doc.createElement(n.tag);
  applyAttrs(el, {}, n.props, ctx, true);
  reconcile(el, [], n.children, ctx);
  applyValues(el, n.props);
  if ("modal" in n.props) scheduleModal(el, n.props.modal, ctx);
  return el;
}

function updateNode(n, o, ctx) {
  const el = o.el;
  if (n.tag === TEXT) {
    if (n.text !== o.text) el.nodeValue = n.text;
    return;
  }
  applyAttrs(el, o.props, n.props, ctx, false);
  reconcile(el, o.children, n.children, ctx);
  applyValues(el, n.props);
  if ("modal" in n.props || "modal" in o.props) scheduleModal(el, !!n.props.modal, ctx);
}

// A <dialog> can only be opened once it is in the document, so the opening is
// an effect that runs after the whole tree has been patched.
function scheduleModal(el, want, ctx) {
  ctx.effects.push(() => {
    if (want && !el.open) el.showModal();
    else if (!want && el.open) el.close();
  });
}

function reconcile(parent, olds, news, ctx) {
  const keyed = new Map();
  const unkeyed = [];
  for (const o of olds) {
    if (o.key !== null) keyed.set(o.key, o);
    else unkeyed.push(o);
  }
  const used = new Set();
  let ui = 0;
  for (const n of news) {
    let o;
    if (n.key !== null) {
      o = keyed.get(n.key);
    } else {
      o = unkeyed[ui++];
    }
    if (o && o.tag === n.tag && !used.has(o)) {
      used.add(o);
      n.el = o.el;
      updateNode(n, o, ctx);
    } else {
      n.el = createNode(n, ctx);
    }
  }
  for (const o of olds) {
    if (!used.has(o)) parent.removeChild(o.el);
  }
  let cursor = parent.firstChild;
  for (const n of news) {
    if (n.el === cursor) cursor = cursor.nextSibling;
    else parent.insertBefore(n.el, cursor);
  }
}

/**
 * Makes `root`'s children match `children`. The previous tree is kept on the
 * root, so the same root is patched on every call.
 * @param {Element} root
 * @param {unknown} children
 */
export function render(root, children) {
  const next = normalize(children);
  const ctx = { doc: root.ownerDocument, effects: [] };
  reconcile(root, root.__vnodes ?? [], next, ctx);
  root.__vnodes = next;
  for (const fx of ctx.effects) fx();
}

// ---- helpers for views and their tests ----

/** Every vnode of a tree, depth first, the root included. */
export function* walk(v) {
  if (v === null || v === undefined) return;
  if (Array.isArray(v)) {
    for (const x of v) yield* walk(x);
    return;
  }
  yield v;
  for (const c of v.children ?? []) yield* walk(c);
}

const INLINE = new Set(["span", "b", "i", "em", "strong", "a", "code", "time", "small", "mark", "kbd", "abbr", "label"]);

/** The text a tree shows: block elements are separated by a space, and white space is collapsed. */
export function textOf(v) {
  const part = (n) => {
    if (n === null || n === undefined) return "";
    if (Array.isArray(n)) return n.map(part).join(" ");
    if (n.tag === TEXT) return n.text;
    const inner = (n.children ?? []).map(part).join("");
    return INLINE.has(n.tag) ? inner : ` ${inner} `;
  };
  return part(v).replace(/\s+/g, " ").trim();
}
