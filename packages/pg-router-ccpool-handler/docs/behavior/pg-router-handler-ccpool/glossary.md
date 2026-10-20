# Glossary — pg-router-handler-ccpool

Terms pg-router's set already defines (**role**, **adapter**, **role object**, **wrap**, **base
environment**, **handler session**, **pre-accept decline**) are **inherited** with their meaning there
(`INV-20`). The words **session**, **pool** and **lease** are the earlier combined handler's and the
session tool's, inherited the same way. This set adds only the terms below.

- **Launcher** — the one executable a role's pool names as its agent executable, rendered for the
  role by the deployment as a store path. It resets the environment to the base keep-list, runs the
  role's wrap one layer per entry, runs the marker stage, and then runs the agent with the arguments
  it was given. The session tool sees it as an opaque executable (`INV-CCA-4`).
- **Keep-list** — the names of the environment variables the launcher lets through its reset: the
  base list and the markers the adapter and the session tool inject, each by **explicit name**, never
  by a prefix (`INV-CCA-5`).
- **Marker stage** — the launcher's last layer before the agent: it writes the per-dispatch **ready
  marker** and then starts the agent. A session that dies without the marker failed before the agent
  started (`INV-CCA-7`).
- **Shared pool** — one pool that serves more than one role. Its roles share one launcher, so for
  them the environment is a property of the pool (`INV-CCA-8`).
- **Cutover** — the moment a role stops being run by the earlier combined handler and starts being
  run by this adapter (`INV-CCA-9`).
