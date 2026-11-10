#!/usr/bin/env python3
"""Prototype mermaid-to-text: flowchart/state via graph-easy, sequence via own renderer.
Usage: m2t.py FILE.mmd  (graph-easy must be on PATH)"""
import re, subprocess, sys

def esc(s):
    return re.sub(r'([\[\]{}<>|#;\\])', r'\\\1', s.strip().strip('"'))

def graph(lines, state):
    names, edges, order = {}, [], []
    def node(tok, side):
        tok = tok.strip()
        if tok == "[*]":
            return "start" if side == "src" else "end"
        m = re.match(r'^(\w+)\s*(?:\[\(?|\{|\(\(?|\[\[)\s*(.*?)\s*(?:\)?\]|\}|\)?\))$', tok)
        if m:
            names[m[1]] = m[2].strip('"')
            return m[1]
        return tok
    for ln in lines:
        ln = ln.strip()
        if not ln or ln.startswith(("%%", "subgraph", "end", "classDef", "class ", "direction", "flowchart", "graph", "stateDiagram")):
            continue
        if state:
            m = re.match(r'^(\S+)\s*-->\s*(\S+?)\s*(?::\s*(.*))?$', ln)
            if m: edges.append((node(m[1], "src"), node(m[2], "dst"), (m[3] or "").strip('" '), "-")); order += [m[1], m[2]]
            continue
        m3 = re.match(r'^(.+?)\s+-\.\s*"(.*?)"\s*\.->\s*(.+)$', ln)
        if m3:
            edges.append((node(m3[1], "src"), node(m3[3], "dst"), m3[2], ".")); continue
        m = re.match(r'^(.+?)\s*(-\.->|-->|---|==>)\s*(?:\|(.*?)\|\s*)?(?:"?-\.?"?\s*)?(.+)$', ln)
        if m:
            a, op, lab, b = m[1], m[2], (m[3] or ""), m[4]
            edges.append((node(a, "src"), node(b, "dst"), lab.strip('"'), "." if "." in op else "-"))
    out = []
    lab = lambda i: esc(names.get(i, i.replace("start", "(start)").replace("end", "(end)")))
    for a, b, l, k in edges:
        arrow = (".. %s ..>" if k == "." else "-- %s -->") % l if l else ("..>" if k == "." else "->")
        out.append("[ %s ] %s [ %s ]" % (lab(a), arrow, lab(b)))
    out.insert(0, "graph { flow: %s; }" % ("south" if (state or not re.search(r"LR|RL", lines[0])) else "east"))
    return subprocess.run(["graph-easy", "--as=boxart"], input="\n".join(out), text=True, capture_output=True).stdout

def seq(lines):
    parts, evs = [], []
    def add(p, label=None):
        if p not in [x[0] for x in parts]: parts.append((p, label or p))
    for ln in lines[1:]:
        ln = ln.strip()
        m = re.match(r'^(?:participant|actor)\s+(\w+)(?:\s+as\s+(.*))?$', ln)
        if m: add(m[1], m[2]); continue
        m = re.match(r'^(\w+)\s*(-->>|->>|-->|->)\s*(\w+)\s*:\s*(.*)$', ln)
        if m: add(m[1]); add(m[3]); evs.append(("msg", m[1], m[3], m[4], "--" in m[2])); continue
        m = re.match(r'^(alt|else|opt|loop|par|end|Note.*?:)\s*(.*)$', ln)
        if m: evs.append(("sep", ln)); continue
    names = [p[1] for p in parts]; idx = {p[0]: i for i, p in enumerate(parts)}
    W = max(max(len(n) for n in names) + 4, 24)
    cx = [W // 2 + i * W for i in range(len(parts))]
    width = cx[-1] + W // 2 + 200
    def base(): return [" "] * width
    def put(row, c, s):
        for i, ch in enumerate(s): row[c + i] = ch
    res = []
    r = base()
    for c, n in zip(cx, names): put(r, c - len(n) // 2, n)
    res.append(r)
    def life():
        r = base()
        for c in cx: r[c] = "│"
        return r
    for e in evs:
        if e[0] == "sep":
            r = life(); put(r, 2, "[" + e[1] + "]"); res.append(r); continue
        _, a, b, t, dash = e
        res.append(life())
        i, j = idx[a], idx[b]
        r = life(); res.append(r)
        if i == j:
            put(r, cx[i] + 1, "─┐ " + t); r2 = life(); put(r2, cx[i] + 1, "◄┘"); res.append(r2); continue
        lo, hi = sorted((cx[i], cx[j]))
        put(r, lo + 1, ("╌" if dash else "─") * (hi - lo - 1))
        r[hi if cx[j] > cx[i] else lo] = "►" if cx[j] > cx[i] else "◄"
        r[cx[i]] = "├" if cx[j] > cx[i] else "┤"
        put(res[-2], lo + 2, t.replace("<br/>", " "))
    res.append(life())
    return "\n".join("".join(r).rstrip() for r in res)

src = open(sys.argv[1]).read().splitlines()
src = [l for l in src if l.strip()]
head = src[0].strip()
if head.startswith("sequenceDiagram"): print(seq(src))
elif head.startswith("stateDiagram"): print(graph(src, True))
else: print(graph(src, False))
