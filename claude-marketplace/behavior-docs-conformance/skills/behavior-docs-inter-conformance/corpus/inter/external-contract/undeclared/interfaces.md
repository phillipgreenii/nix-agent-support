# Interfaces — implementer (inter corpus: external-contract UNDECLARED)

This set USES an external tool (`git`) in prose but does NOT declare the consumed contract in an
imports table (its `## External references` table is empty). The mechanical layer sees "no external
references"; catching that a USED tool is undeclared is the agent's judgment (it reads the prose).

The core commits work to a `git` branch and pushes it, yet declares no git contract below.

## External references

| Name | What it is | Owner set-path | Owner UUID |
| ---- | ---------- | -------------- | ---------- |

INTER expectation: agent FLAGS an undeclared consumed external contract (`git` is used but not
declared); the mechanical layer reports "declares no external references".
