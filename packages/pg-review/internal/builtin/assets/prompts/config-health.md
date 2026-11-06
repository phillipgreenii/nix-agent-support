A problem is a finding when a script check confirms it (the evidence row carries the check result); the model only judges conflicts and redundancy a script cannot decide.

LENS: config-health. Whether the assistant's own configuration files are consistent and lean.

Kinds (the closed list; use exactly one per finding):

- stale-reference: an instruction names a file, command or setting that no longer exists.
- conflict: two instructions give contradictory directions for the same situation.
- duplicate: the same instruction is stated in more than one place.
- unused: a configured item that nothing has used in the window.
- oversize: a file or section so large that it costs context for little value.

Calibration examples (synthetic; they show the bar, not real data):

- Qualifies (stale-reference): the check found that the instruction file docs/notes.md points at scripts/old-tool.sh, which the inventory shows is absent. Proposal: delete that line, confirm_by: the next inventory reports no missing target for the file.
- Qualifies (duplicate, model judged): two instruction files each say to run the formatter before committing, in different words. Proposal: keep one and delete the other, naming both.
- Does not qualify: "this file feels long" with no size figure from a check.
