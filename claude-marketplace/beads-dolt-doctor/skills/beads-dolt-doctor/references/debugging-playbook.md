# Beads/dolt debugging playbook

Read-only diagnosis first; destructive steps only after data safety is
confirmed. None of these steps start a server.

## 1. Enumerate the dolt processes

```bash
ps -axo pid,ppid,lstart,command | grep '[d]olt sql-server'
```

Look for **two** servers: one with a `--config <path>` (the shared server) and
one **config-less** rooted at a workspace-local `./.beads/dolt`. Note the
**start times** (`lstart`) — the newer one is usually the rogue — and each
process's **PPID** (the parent that spawned it).

## 2. Find the port holder

```bash
lsof -nP -iTCP:25252 | grep LISTEN
```

Whichever PID is `LISTEN`ing owns `127.0.0.1:25252`. If it is not the shared
`org.nixos.beads-dolt-server` process, that is the rogue.

## 3. Check the shared agent's health

```bash
launchctl list org.nixos.beads-dolt-server
```

A changing **PID** across repeated calls plus `LastExitStatus = 256` means the
agent is crash-looping (respawned by `keepAlive`, failing on "Port 25252 already
in use" because the rogue holds it).

## 4. Read both dolt-server logs

Read the shared server's log and the rogue's log (each server writes its own
`dolt-server.log` under its data root). The "Port already in use" / bind-failure
lines separate the crash-looping real server from the squatting rogue.

## 5. Narrow the caller

The rogue was started by _something_. Find it:

- **Enumerate launchd jobs** and any `pg-launchd`-wrapped daemons; look for jobs
  that shell out to `bd` on a timer (e.g. a PR-sync / maintenance daemon).
- **Check Claude hooks** and editor/IDE **beads extensions** (e.g.
  `planet57.vscode-beads`) that poll `bd dolt status` and run `bd dolt start` on
  connect / on error.
- Run a fast `ps` **spawn-catcher** to catch the ephemeral `bd`/`dolt` process
  and record its **PPID and parent command** before it exits:

  ```bash
  while true; do
    ps -axo pid,ppid,lstart,command \
      | grep -E '[b]d dolt start|[d]olt sql-server' \
      && break
    sleep 0.05
  done
  ```

  Then map the captured PPID back to its parent (`ps -p <ppid> -o command=`) to
  identify the launchd job / editor window / Claude session responsible.

- **Shut down suspects one at a time** (a daemon, an editor window, a Claude
  session) and watch whether the rogue stops reappearing.

## 6. Confirm data safety BEFORE killing anything

Never kill a server before you know which one has the real data:

```bash
du -sh ~/.local/share/beads-dolt        # shared data (expect large)
du -sh ./.beads/dolt                    # workspace-local (expect tiny/empty)
bd stats                                # issue counts — sanity-check which DB bd hit
```

Only after confirming the shared data is intact and the rogue points at a tiny
empty DB should you kill the rogue PID and let `org.nixos.beads-dolt-server`
rebind 25252.

## 7. Fix and prevent

- Remove/guard the caller (declaratively remove the auto-starting extension).
- Ensure the machine-wide `bd` exports `BEADS_DOLT_AUTO_START=0` (overlay
  wrapper), reaching shells, GUI apps, and launchd daemons alike.
- Keep `org.nixos.beads-dolt-server` `keepAlive=true` so it holds the port — a
  **mitigation** (downgrades stray explicit starts to no-ops while it holds the
  port), not a guarantee; brief restart/activation windows leave the port free.
