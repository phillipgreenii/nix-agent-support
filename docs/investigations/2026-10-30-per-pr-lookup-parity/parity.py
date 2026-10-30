#!/usr/bin/env python3
"""Per-PR lookup parity spike (bead pg2-eclw3j.1; re-run by bead pg2-eclw3j.8).

Read-only. Only `bd list` is called (never a mutating verb). For every PR the
tracker has a work item for, compares

  (a) what the decider selects today: the union of the six work-items-all
      expressions, filtered by belongs() (packages/pg-decider/internal/apply/
      lookup.go:213-228), with
  (b) the per-PR expressions E1, E2 and E3 (each raw, and after belongs()).

Usage:
  python3 -I parity.py [--tracker DIR] [--out DIR] [--workers N]

Outputs under --out: per-pr.json, differences.tsv, e3-bound.json, summary.json,
summary.md. Stdlib only. Values compared with str() on pr_number (a string on
some beads, a JSON number on others).
"""

import argparse
import collections
import hashlib
import json
import os
import re
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor

DEFAULT_TRACKER = "/Volumes/gitrepos/ziprecruiter/pristine"
KINDS = ["anchor", "process-feedback", "review-pr", "fix-ci", "resolve-conflict", "focus-item"]
TITLE_KINDS = ["process-feedback", "review-pr", "fix-ci", "resolve-conflict"]
# The six work-items-all expressions (nix-ziprecruiter machines/phillipg-mbp-02),
# with --all in place of the explicit status list.
WORK_ITEMS_ALL = [
    ["--type", "merge-request"],
    ["--type", "task", "--title-contains", "process-feedback:"],
    ["--type", "task", "--title-contains", "review-pr:"],
    ["--type", "task", "--title-contains", "fix-ci:"],
    ["--type", "task", "--title-contains", "resolve-conflict:"],
    ["--label", "focus-item"],
]
REF_RE = re.compile(r"([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#(\d+)")
TIMEOUT = 90


CACHE = None  # directory of cached bd answers (resume after a failure); None = off


def bd(tracker, args):
    cmd = ["bd", "-C", tracker, "list", "--all", "--limit", "0", "--json"] + args
    path = None
    if CACHE:
        path = os.path.join(CACHE, hashlib.sha1("\0".join(cmd).encode()).hexdigest() + ".json")
        if os.path.exists(path):
            with open(path) as f:
                return json.load(f)
    last = ""
    for attempt, pause in enumerate((0, 5, 20, 45)):
        if pause:
            time.sleep(pause)
        try:
            p = subprocess.run(cmd, capture_output=True, text=True, timeout=TIMEOUT)
        except subprocess.TimeoutExpired:
            last = "timeout"
            continue
        if p.returncode == 0:
            data = json.loads(p.stdout).get("data") or []
            if path:
                with open(path, "w") as f:
                    json.dump(data, f)
            return data
        last = f"exit {p.returncode}: {p.stderr[:200]}"
    raise RuntimeError(f"{' '.join(cmd)}: {last}")


def mdstr(v):
    """The text the decider reads a metadata value as (lookup.go item())."""
    if v is None:
        return ""
    if isinstance(v, str):
        return v
    return json.dumps(v)


def norm(row):
    md = {k: mdstr(v) for k, v in (row.get("metadata") or {}).items()}
    md_types = {k: type(v).__name__ for k, v in (row.get("metadata") or {}).items()}
    return {
        "id": row["id"], "title": row.get("title", ""), "state": row.get("status", ""),
        "labels": row.get("labels") or [], "md": md, "mdt": md_types,
        "type": row.get("issue_type", ""),
    }


def parse_key(key):
    parts = key.split(":", 3)
    if len(parts) < 3 or not parts[0] or not parts[1] or parts[2] not in KINDS:
        return None
    return parts[0], parts[1], parts[2]


def title_names(title, ref):
    for k in TITLE_KINDS:
        base = f"{k}: {ref}"
        if title == base or title.startswith(base + " @ "):
            return True
    return title.startswith(ref + ": ")


def kind_from_title(title):
    for k in TITLE_KINDS:
        if title.startswith(k + ": "):
            return k
    i = title.find("#")
    if i > 0:
        m = re.match(r"\d+: ", title[i + 1:])
        if m:
            return "anchor"
    return None


def belongs(it, repo, num, node_ids):
    ref = f"{repo}#{num}"
    k = it["md"].get("dedup_key", "")
    pk = parse_key(k) if k else None
    if pk:
        return pk[0] == "pr" and (pk[1] == ref or pk[1] in node_ids)
    if it["md"].get("repo") == repo and it["md"].get("pr_number") == str(num):
        return True
    if node_ids and it["md"].get("node_id") in node_ids:
        return True
    return title_names(it["title"], ref)


def in_index(it, repo, num, node_ids):
    """Approximates workitem.BuildIndex membership of a belongs()-selected item:
    keyed items, or adoptable ones (node_id, title, anchor prefix)."""
    k = it["md"].get("dedup_key", "")
    if k and parse_key(k):
        return True
    ref = f"{repo}#{num}"
    node = it["md"].get("node_id", "")
    if node_ids and node:
        return node in node_ids and kind_from_title(it["title"]) is not None
    return title_names(it["title"], ref)


def derive_prs(it):
    """All (repo, number-string) identities one row carries, with the route."""
    out = []
    k = it["md"].get("dedup_key", "")
    pk = parse_key(k) if k else None
    if pk and pk[0] == "pr":
        m = REF_RE.fullmatch(pk[1])
        if m:
            out.append((m.group(1), m.group(2), "key"))
    r, n = it["md"].get("repo"), it["md"].get("pr_number")
    if r and n and re.fullmatch(r"\d+", n):
        out.append((r, n, "meta"))
    t = it["title"]
    for kind in TITLE_KINDS:
        if t.startswith(kind + ": "):
            m = REF_RE.match(t[len(kind) + 2:])
            if m:
                out.append((m.group(1), m.group(2), "title"))
    m = re.match(r"([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#(\d+): ", t)
    if m:
        out.append((m.group(1), m.group(2), "title"))
    return out


def classify_gap(it, repo, num, found_e3, pr_routes):
    """Why E1 and E2 did not return an item the decider selects."""
    md = it["md"]
    k = md.get("dedup_key", "")
    pk = parse_key(k) if k else None
    keyed = bool(pk)
    r, n = md.get("repo"), md.get("pr_number")
    ref = f"{repo}#{num}"
    focus = "focus-item" in it["labels"]
    if focus:
        if md.get("source_type") != "pr" or md.get("source_id") != ref:
            cause = "focus-item without matching source_type/source_id"
        else:
            cause = "focus-item (E2 expected to match; unexplained)"
    elif keyed and pk[1] != ref and "#" not in pk[1]:
        cause = "node_id-only identification (key ident is a node id)"
        if not (r and n):
            cause += ", no repo/pr_number metadata"
    elif r is None and n is None:
        cause = ("keyed child without repo/pr_number metadata" if keyed
                 else "keyless legacy item without repo/pr_number metadata (title match)")
    elif r is None or n is None:
        cause = "partial repo/pr_number metadata (one key missing)"
    elif r.lower() == repo.lower() and r != repo:
        cause = "case variant of repo in metadata"
    elif n.lstrip("0") == num and n != num:
        cause = "leading-zero variant of pr_number"
    elif r != repo and n == num:
        cause = "renamed repo (metadata repo differs, same number)"
    elif r == repo and n != num:
        cause = "stale pr_number (metadata names another number)"
    else:
        cause = "other metadata mismatch"
    return cause + (" [E3 finds it]" if found_e3 else " [E3 does NOT find it]")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tracker", default=DEFAULT_TRACKER)
    ap.add_argument("--out", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "out"))
    ap.add_argument("--all-prs", action="store_true", help="also PRs with no dedup_key (old merge-request anchors)")
    ap.add_argument("--workers", type=int, default=2)
    a = ap.parse_args()
    os.makedirs(a.out, exist_ok=True)
    global CACHE
    CACHE = os.path.join(a.out, "cache")  # delete it for a fresh run
    os.makedirs(CACHE, exist_ok=True)

    # (a) baseline: the six expressions, union by id
    base = {}
    per_expr = []
    for ex in WORK_ITEMS_ALL:
        rows = bd(a.tracker, ex)
        per_expr.append(len(rows))
        for r in rows:
            base.setdefault(r["id"], norm(r))
    items = list(base.values())
    print(f"work-items-all: {per_expr} rows, {len(items)} distinct", file=sys.stderr)

    # PR identities; node-id map
    pr_items = collections.defaultdict(set)  # lower-cased (repo, n) -> ids
    repo_case = collections.defaultdict(collections.Counter)
    prs_with_key = set()
    for it in items:
        for repo, n, route in derive_prs(it):
            key = (repo.lower(), str(int(n)))
            pr_items[key].add(it["id"])
            repo_case[key][repo] += 1
            if route == "key":
                prs_with_key.add(key)
    # Node ids: no bead carries node_id metadata, so a PR's node id is the ident
    # of the node-form dedup_keys on rows that name the PR by metadata or title.
    node_of = collections.defaultdict(set)
    node_keyed = []  # (row id, node ident)
    for it in items:
        k = it["md"].get("dedup_key", "")
        pk = parse_key(k) if k else None
        if not (pk and pk[0] == "pr" and "#" not in pk[1]):
            continue
        node_keyed.append((it["id"], pk[1]))
        for repo, n, route in derive_prs(it):
            if route != "key":
                node_of[(repo.lower(), str(int(n)))].add(pk[1])
    node_to_pr = {nd: key for key, nds in node_of.items() for nd in nds}
    unresolved_node_keys = []
    for rid, nd in node_keyed:
        key = node_to_pr.get(nd)
        if key:
            pr_items[key].add(rid)
            prs_with_key.add(key)
        else:
            unresolved_node_keys.append({"id": rid, "node_ident": nd})

    prs = sorted(pr_items) if a.all_prs else sorted(prs_with_key)
    print(f"{len(prs)} PRs analysed ({len(prs_with_key)} with a dedup_key, {len(pr_items)} seen)", file=sys.stderr)

    # (b) per-PR expressions
    def fetch(key):
        repo = repo_case[key].most_common(1)[0][0]
        num = key[1]
        ref = f"{repo}#{num}"
        e1 = bd(a.tracker, ["--metadata-field", f"repo={repo}", "--metadata-field", f"pr_number={num}"])
        e2 = bd(a.tracker, ["--metadata-field", "source_type=pr", "--metadata-field", f"source_id={ref}"])
        e3 = bd(a.tracker, ["--title-contains", ref])
        return key, repo, num, e1, e2, e3

    with ThreadPoolExecutor(max_workers=a.workers) as ex:
        fetched = list(ex.map(fetch, prs))

    per_pr = []
    diffs = []  # (cause, bead id, pr)
    extras = collections.Counter()
    extra_ex = collections.defaultdict(list)
    for key, repo, num, e1, e2, e3 in fetched:
        node_ids = node_of.get(key, set())
        sel = {i for i, it in base.items() if belongs(it, repo, num, node_ids)}
        sel_idx = {i for i in sel if in_index(base[i], repo, num, node_ids)}
        ids = {"e1": {r["id"] for r in e1}, "e2": {r["id"] for r in e2}, "e3": {r["id"] for r in e3}}
        rows = {r["id"]: norm(r) for r in e1 + e2 + e3}
        e12 = ids["e1"] | ids["e2"]
        e123 = e12 | ids["e3"]
        filt = {i for i in e123 if belongs(rows[i], repo, num, node_ids)}
        miss12 = sel - e12
        miss123 = sel - e123
        miss12_idx = sel_idx - e12
        miss123_idx = sel_idx - e123
        for i in sorted(miss12):
            cause = classify_gap(base[i], repo, num, i in ids["e3"], None)
            diffs.append((cause, i, f"{repo}#{num}", i in sel_idx))
        for i in sorted(filt - sel):
            extras["extra: belongs() passes, not in work-items-all"] += 1
            extra_ex["extra: belongs() passes, not in work-items-all"].append(i)
        for i in sorted(e123 - filt):
            tag = "extra raw row rejected by belongs() (" + ",".join(
                n for n in ("e1", "e2", "e3") if i in ids[n]) + ")"
            extras[tag] += 1
            extra_ex[tag].append(i)
        per_pr.append({
            "pr": f"{repo}#{num}", "digits": len(num), "has_key": key in prs_with_key,
            "baseline_selected": len(sel), "baseline_index_items": len(sel_idx),
            "e1_raw": len(ids["e1"]), "e2_raw": len(ids["e2"]), "e3_raw": len(ids["e3"]),
            "union_raw": len(e123), "union_belongs": len(filt),
            "missing_e1e2": len(miss12), "missing_e1e2e3": len(miss123),
            "missing_e1e2_index": len(miss12_idx), "missing_e1e2e3_index": len(miss123_idx),
            "missing_e1e2e3_ids": sorted(miss123), "missing_e1e2_ids": sorted(miss12),
        })

    # E3 bound: row count of title-contains "<repo>#<N>" for every number prefix seen
    allrows = bd(a.tracker, [])
    cnt = collections.defaultdict(set)  # (repo lower, prefix) -> ids
    for r in allrows:
        for m in REF_RE.finditer(r.get("title", "")):
            d = m.group(2)
            for j in range(1, len(d) + 1):
                cnt[(m.group(1).lower(), d[:j])].add(r["id"])
    by_digits = collections.defaultdict(list)
    for (repo, pre), ids_ in cnt.items():
        by_digits[len(pre)].append((len(ids_), repo, pre))
    e3b = {}
    for d in sorted(by_digits):
        v = sorted(by_digits[d], reverse=True)
        e3b[d] = {"max_rows": v[0][0], "worst": f"{v[0][1]}#{v[0][2]}",
                  "count_over_100": sum(1 for x in v if x[0] > 100),
                  "count_over_50": sum(1 for x in v if x[0] > 50), "prefixes_seen": len(v)}
    live = {}
    for d in sorted(by_digits):
        e3raw = [p["e3_raw"] for p in per_pr if p["digits"] == d]
        if e3raw:
            live[d] = {"prs": len(e3raw), "max": max(e3raw), "min": min(e3raw)}

    # outputs
    json.dump(per_pr, open(os.path.join(a.out, "per-pr.json"), "w"), indent=1)
    json.dump({"offline_prefix_counts_by_digits": e3b, "live_e3_raw_by_digits": live,
               "all_beads": len(allrows)}, open(os.path.join(a.out, "e3-bound.json"), "w"), indent=1)
    with open(os.path.join(a.out, "differences.tsv"), "w") as f:
        f.write("cause\tbead_id\tpr\tin_index\n")
        for c, i, p, ix in diffs:
            f.write(f"{c}\t{i}\t{p}\t{ix}\n")
    by_cause = collections.defaultdict(list)
    for c, i, p, ix in diffs:
        by_cause[c].append(i)
    table = [{"cause": c, "beads": len(set(v)), "examples": sorted(set(v))[:5]} for c, v in
             sorted(by_cause.items(), key=lambda kv: -len(set(kv[1])))]
    prs_need_e3 = sorted({p for c, i, p, ix in diffs if "[E3 finds it]" in c})
    prs_need_check = sorted({p for c, i, p, ix in diffs if "[E3 does NOT find it]" in c})
    summary = {
        "work_items_all_rows_per_expression": per_expr, "work_items_all_distinct": len(items),
        "prs": len(prs), "prs_with_dedup_key": len(prs_with_key),
        "pr_node_id_only_unresolved_keys": unresolved_node_keys,
        "difference_table": table,
        "prs_needing_e3": prs_need_e3, "prs_needing_consistency_check": prs_need_check,
        "prs_with_gap_e1e2": sum(1 for p in per_pr if p["missing_e1e2"]),
        "prs_with_gap_e1e2e3": sum(1 for p in per_pr if p["missing_e1e2e3"]),
        "prs_with_index_gap_e1e2": sum(1 for p in per_pr if p["missing_e1e2_index"]),
        "prs_with_index_gap_e1e2e3": sum(1 for p in per_pr if p["missing_e1e2e3_index"]),
        "extras": [{"kind": k, "rows": v, "examples": sorted(set(extra_ex[k]))[:5]} for k, v in extras.items()],
        "raw_rows": {n: {"max": max(p[n] for p in per_pr), "median": sorted(p[n] for p in per_pr)[len(per_pr) // 2],
                         "total": sum(p[n] for p in per_pr)} for n in ("e1_raw", "e2_raw", "e3_raw", "union_belongs")},
    }
    # Population facts over ALL work-items-all rows (not only the analysed PRs):
    # keyless rows (legacy, not decider-minted) by whether E1 could ever see them.
    pop = collections.Counter()
    pop_ex = collections.defaultdict(list)
    for it in items:
        if "focus-item" in it["labels"]:
            continue
        k = it["md"].get("dedup_key", "")
        keyed = bool(k and parse_key(k))
        has_meta = bool(it["md"].get("repo") and it["md"].get("pr_number"))
        tag = ("keyed" if keyed else "keyless") + ("+repo/pr_number metadata" if has_meta else ", NO repo/pr_number metadata")
        tag += f" [{'closed' if it['state'] == 'closed' else 'open'}]"
        pop[tag] += 1
        pop_ex[tag].append(it["id"])
        if it["mdt"].get("pr_number"):
            pop["pr_number stored as " + it["mdt"]["pr_number"]] += 1
    # Offline simulation over EVERY PR seen (incl. the old keyless merge-request
    # PRs): E1/E2 are modelled as exact metadata equality over the work-items-all
    # rows (validated live above on the analysed PRs), so this shows which PRs
    # outside the decider-minted set would need E3 or the consistency check.
    sim_causes = collections.defaultdict(list)
    sim_prs = collections.defaultdict(set)
    for key in sorted(pr_items):
        repo = repo_case[key].most_common(1)[0][0]
        num = key[1]
        nds = node_of.get(key, set())
        for i, it in base.items():
            if not belongs(it, repo, num, nds):
                continue
            md = it["md"]
            e1 = md.get("repo") == repo and md.get("pr_number") == num
            e2 = "focus-item" in it["labels"] and md.get("source_type") == "pr" and md.get("source_id") == f"{repo}#{num}"
            if e1 or e2:
                continue
            c = classify_gap(it, repo, num, True, None).replace(" [E3 finds it]", "")
            sim_causes[c].append(i)
            sim_prs[c].add(f"{repo}#{num}")
    summary["offline_all_prs_simulation"] = {
        "prs_seen": len(pr_items),
        "gaps": [{"cause": c, "beads": len(set(v)), "prs": len(sim_prs[c]), "examples": sorted(set(v))[:5]}
                 for c, v in sorted(sim_causes.items(), key=lambda kv: -len(set(kv[1])))],
    }
    summary["population_work_items_all"] = {t: {"rows": c, "examples": pop_ex[t][:5]} for t, c in sorted(pop.items())}
    json.dump(summary, open(os.path.join(a.out, "summary.json"), "w"), indent=1)
    with open(os.path.join(a.out, "summary.md"), "w") as f:
        f.write("| difference cause | bead count | example ids |\n|---|---|---|\n")
        for t in table:
            f.write(f"| {t['cause']} | {t['beads']} | {', '.join(t['examples'])} |\n")
    print(json.dumps({k: summary[k] for k in ("prs", "prs_with_dedup_key", "prs_with_gap_e1e2",
                                               "prs_with_gap_e1e2e3", "prs_with_index_gap_e1e2",
                                               "prs_with_index_gap_e1e2e3")}, indent=1))


if __name__ == "__main__":
    main()
