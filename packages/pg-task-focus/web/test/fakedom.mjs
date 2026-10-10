// A very small DOM, enough for the patcher: elements and text nodes with the
// tree operations and the properties vdom.mjs uses. It exists so the patcher
// and the whole page can be exercised under node's test runner without a
// browser. It is not a browser: it knows nothing of layout, events or focus
// order beyond what is written here.

class FakeNode {
  constructor(doc) {
    this.ownerDocument = doc;
    this.parentNode = null;
    this.childNodes = [];
  }
  get firstChild() {
    return this.childNodes[0] ?? null;
  }
  get nextSibling() {
    if (!this.parentNode) return null;
    const s = this.parentNode.childNodes;
    return s[s.indexOf(this) + 1] ?? null;
  }
  removeChild(c) {
    const i = this.childNodes.indexOf(c);
    if (i < 0) throw new Error("removeChild: not a child");
    this.childNodes.splice(i, 1);
    c.parentNode = null;
    if (this.ownerDocument.activeElement === c || c.contains?.(this.ownerDocument.activeElement)) this.ownerDocument.activeElement = null;
    return c;
  }
  insertBefore(c, ref) {
    if (c.parentNode) c.parentNode.childNodes.splice(c.parentNode.childNodes.indexOf(c), 1);
    const i = ref === null || ref === undefined ? this.childNodes.length : this.childNodes.indexOf(ref);
    if (i < 0) throw new Error("insertBefore: reference is not a child");
    this.childNodes.splice(i, 0, c);
    c.parentNode = this;
    this.ownerDocument.moves++;
    return c;
  }
  appendChild(c) {
    return this.insertBefore(c, null);
  }
  contains(n) {
    for (let x = n; x; x = x.parentNode) if (x === this) return true;
    return false;
  }
  get isConnected() {
    for (let x = this; x; x = x.parentNode) if (x === this.ownerDocument.body) return true;
    return false;
  }
}

class FakeText extends FakeNode {
  constructor(doc, text) {
    super(doc);
    this.nodeValue = text;
    this.nodeType = 3;
  }
  get textContent() {
    return this.nodeValue;
  }
}

class FakeElement extends FakeNode {
  constructor(doc, tag) {
    super(doc);
    this.tagName = tag.toUpperCase();
    this.nodeType = 1;
    this.attrs = new Map();
    this.value = "";
    this.checked = false;
    this.selected = false;
    this.disabled = false;
    this.hidden = false;
    this.required = false;
    this.readOnly = false;
    this.open = false;
    this.modalOpens = 0;
  }
  setAttribute(n, v) {
    this.attrs.set(n, String(v));
  }
  removeAttribute(n) {
    this.attrs.delete(n);
  }
  getAttribute(n) {
    return this.attrs.has(n) ? this.attrs.get(n) : null;
  }
  focus() {
    if (!this.isConnected) return;
    this.ownerDocument.activeElement = this;
  }
  showModal() {
    if (!this.isConnected) throw new Error("InvalidStateError: showModal on a dialog that is not in the document");
    this.open = true;
    this.modalOpens++;
  }
  close() {
    this.open = false;
  }
  get textContent() {
    return this.childNodes.map((c) => c.textContent).join("");
  }
  *descendants() {
    for (const c of this.childNodes) {
      yield c;
      if (c.descendants) yield* c.descendants();
    }
  }
  /** Descendants with a tag (case-insensitive). */
  byTag(tag) {
    return [...this.descendants()].filter((n) => n.tagName === tag.toUpperCase());
  }
}

export class FakeDocument {
  constructor() {
    this.moves = 0;
    this.activeElement = null;
    this.body = new FakeElement(this, "body");
    this.title = "";
  }
  createElement(tag) {
    return new FakeElement(this, tag);
  }
  createTextNode(text) {
    return new FakeText(this, text);
  }
}

/** A fresh document with a root element in its body. */
export function newRoot() {
  const doc = new FakeDocument();
  const root = doc.createElement("div");
  doc.body.appendChild(root);
  return { doc, root };
}
