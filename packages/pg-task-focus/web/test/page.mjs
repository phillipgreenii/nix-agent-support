// Reading and driving the page as data. The page is a tree of plain objects,
// so a test finds a control by what a person (or a screen reader) would call
// it, presses it by calling its handler and types by calling its input handler.
// This is not a browser: it proves what the page says and which controls are
// enabled, and that a press raises the right intent, not how it looks.

import { textOf, walk } from "../assets/vdom.mjs";

/** Whether a vnode is an element of the given tag. */
const isTag = (n, tag) => n.tag === tag;

/** The accessible name of a control: its aria-label, else its text. */
export function nameOf(n) {
  return (n.props["aria-label"] ?? textOf(n)).trim();
}

export function page(tree) {
  const all = () => [...walk(tree)].filter((n) => n.tag !== "#text");
  const api = {
    tree,
    all,
    text: () => textOf(tree),
    /** Elements matching a predicate. */
    where: (pred) => all().filter(pred),
    /** Buttons by accessible name (string: exact; RegExp: match). */
    buttons(name) {
      return all().filter((n) => isTag(n, "button") && (name instanceof RegExp ? name.test(nameOf(n)) : nameOf(n) === name));
    },
    button(name) {
      const found = api.buttons(name);
      if (found.length === 0) throw new Error(`no button named ${name}; buttons are: ${all().filter((n) => isTag(n, "button")).map(nameOf).join(" | ")}`);
      if (found.length > 1) throw new Error(`${found.length} buttons are named ${name}`);
      return found[0];
    },
    hasButton: (name) => api.buttons(name).length > 0,
    /** Presses a button by name. */
    async click(name) {
      const b = api.button(name);
      if (b.props.disabled) throw new Error(`the button ${name} is disabled`);
      return b.props.onclick?.({ preventDefault() {} });
    },
    /** The control a label names. */
    control(labelText) {
      const label = all().find((n) => isTag(n, "label") && textOf(n).replace(/\s+/g, " ").trim() === labelText);
      if (!label) throw new Error(`no label ${labelText}; labels are: ${all().filter((n) => isTag(n, "label")).map((n) => textOf(n)).join(" | ")}`);
      const c = all().find((n) => n.props.id === label.props.for);
      if (!c) throw new Error(`label ${labelText} names no control`);
      return c;
    },
    /** Types into a control by its label. */
    type(labelText, value) {
      const c = api.control(labelText);
      if (c.props.disabled) throw new Error(`the field ${labelText} is disabled`);
      const ev = { target: { value, checked: value }, preventDefault() {} };
      return (c.props.oninput ?? c.props.onchange)?.(ev);
    },
    /** Submits the form that has a control with the label. */
    submitOf(formLabel) {
      const f = all().find((n) => isTag(n, "form") && n.props["aria-label"] === formLabel);
      if (!f) throw new Error(`no form ${formLabel}`);
      return f.props.onsubmit({ preventDefault() {} });
    },
    byAttr: (name, value) => all().filter((n) => n.props[name] === value),
    /** Controls that mutate. */
    mutators: () => all().filter((n) => n.props["data-mutates"] === "true"),
    /** Whether anything on the page mentions text. */
    has: (s) => api.text().includes(s),
  };
  return api;
}

/** Walks a fake DOM and finds an element by an attribute (the test's `find` for the app glue). */
export function findIn(root, attr, value) {
  for (const n of root.descendants()) if (n.getAttribute?.(attr) === value) return n;
  return null;
}
