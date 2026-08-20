# shellcheck shell=bash
# imports-row.bash — the shared GFM-table row/cell parsing for a `## External
# references` imports table. ONE definition, so `resolve-imports.sh` (identity
# resolution: does the cited owner UUID resolve, and does the cited NAME still
# match?) and `resolve-links.sh` (bead pg2-2oupw: does the D5 remote-url link
# still carry that UUID?) read the SAME two live shapes the same way. Extracted
# from `resolve-imports.sh` (bead pg2-0pjvu built `cell_uuid`) rather than
# duplicated, because this plugin has already paid for that drift once — see
# `behavior-ids.bash`'s header (bead pg2-fbxdw, the typed-id family list drifted
# twice across eight inlined copies).
#
# Two imports-table shapes are accepted, detected PER ROW (see row_cell/cell_uuid),
# so a table part-way through the migration between them still resolves every row:
#
#   | Name | Owner set-path | Owner UUID |                      (current)
#   | Name | What it is | Owner set-path | [<uuid>](remote-url) | (D5)
#
# The owner UUID is the LAST visible cell and the owner set-path the one before it
# in both, so the owner cells are read from the RIGHT rather than by fixed index.
#
# DEPENDENCY: `cell_uuid` reads `$UUIDRE`, a caller-defined variable (this file
# defines no UUID shape of its own — every caller already needs its own copy for
# other matching, e.g. a carrier-line scan, so defining it here too would just be
# a second place for that pattern to drift). Source this file AFTER setting
# `UUIDRE='[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}'`
# or the equivalent.

trim() { sed -E 's/^[[:space:]]+//; s/[[:space:]]+$//'; }

# row_cell <gfm-row> <index> — print ONE cell of a leading-pipe GFM table row.
# A POSITIVE index counts visible cells from the LEFT (1 = the first); a NEGATIVE
# one counts from the RIGHT (-1 = the last, -2 = the one before it). awk's field 1
# is the empty string before the leading pipe, and when the row also ends with a
# pipe its LAST field is the empty string after it; both are dropped so the index
# is over VISIBLE cells. Only an exactly-empty trailing field is dropped, so a
# genuinely blank last cell (`| … | … |  |`) still counts as a cell.
#
# Reading the owner cells from the RIGHT is what makes this parser shape-agnostic.
# The imports table's owner UUID is the LAST visible cell and the owner set-path
# the one before it in BOTH live shapes: the current
# `| Name | Owner set-path | Owner UUID |` and D5's
# `| Name | What it is | Owner set-path | [<uuid>](remote-url) |`, which inserts a
# column as the SECOND visible cell and so shifts both owner cells one field right.
row_cell() {
  printf '%s\n' "$1" | awk -F'|' -v i="$2" '
    {
      n = NF
      if (n > 1 && $n == "") n--
      k = (i + 0 < 0) ? n + 1 + i : 1 + i
      if (k >= 2 && k <= n) print $k
    }'
}

# cell_uuid <owner-uuid-cell> — the owner UUID the cell DECLARES, or nothing.
# Two shapes are accepted, detected on the CELL ITSELF (never on a header or a
# per-table mode) so a table MID-MIGRATION whose rows mix the shapes still
# resolves row by row:
#   bare      `<uuid>`                — the current shape
#   D5 link   `[<uuid>](remote-url)`  — the shape D5 introduces
# For the link form the identity is the LINK TEXT and ONLY the link text: a
# remote-url may itself carry a UUID (a fragment, a permalink path), and "the
# first UUID anywhere in the cell" would let that masquerade as the declared
# identity. A cell that IS a link but whose text is not a well-formed UUID
# therefore yields NOTHING — the caller MUST treat an empty result as a failure,
# never as a pass (that silent pass is the whole defect this parser change fixes).
cell_uuid() {
  local cell u=''
  cell=$(printf '%s' "$1" | tr -d '`')
  if printf '%s' "$cell" | grep -q ']('; then
    u=$(printf '%s' "$cell" | sed -nE "s|.*\[[[:space:]]*($UUIDRE)[[:space:]]*\][[:space:]]*\(.*|\1|p" | head -1) || u=''
  else
    u=$(printf '%s' "$cell" | grep -oE "$UUIDRE" | head -1) || u=''
  fi
  printf '%s' "$u"
}

# cell_url <owner-uuid-cell> — the `(remote-url)` half of a D5 link cell, or
# nothing for the bare-UUID shape (there is no url to read) or a malformed link.
# Companion to `cell_uuid`: together they split a D5 cell into its two halves
# without either half leaking into the other (the url is never scanned for a
# UUID — that is `cell_uuid`'s whole point — and the identity text is never
# treated as a fetchable location).
cell_url() {
  local cell u=''
  cell=$(printf '%s' "$1" | tr -d '`')
  if printf '%s' "$cell" | grep -q ']('; then
    u=$(printf '%s' "$cell" | sed -nE 's|.*\]\(([^)]*)\).*|\1|p' | head -1) || u=''
  fi
  printf '%s' "$u"
}
