# The shared owner set — impl corpus

The behavior-docs set every fixture in this corpus is reconciled against. It defines two
invariants and one interface, and declares one external element, so a fixture can exercise each
classification `impl-traces.sh` makes: resolving locally, resolving through the imports table,
framed as historical, and dangling.

## External references

| Name     | What it is                                     | Owner set-path               | Owner UUID                           |
| -------- | ---------------------------------------------- | ---------------------------- | ------------------------------------ |
| `INV-90` | the owner rule this set defers to for identity | `other-repo · docs/behavior` | 90909090-9090-4909-8909-909090909090 |
