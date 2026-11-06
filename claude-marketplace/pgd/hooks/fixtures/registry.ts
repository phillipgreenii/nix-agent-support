// Sample session registry entries (~/.claude/sessions/<pid>.json), as exported strings: the
// engine's test runtime has no node:fs, so a module both runners can import is the common shape.
// The shape follows Claude Code 2.1.314: sessionId, name, nameSource, nameSince.

// An entry whose name the user set (claude -n <name>, or /rename).
export const ENTRY_USER = JSON.stringify({
  pid: 4242,
  sessionId: 'sess-1',
  name: 'drain-bead',
  nameSource: 'user',
  nameSince: 1762400000000,
})

// An entry whose name was derived from the working directory: no name is shown.
export const ENTRY_DERIVED = JSON.stringify({
  pid: 4242,
  sessionId: 'sess-1',
  name: 'repo',
  nameSource: 'derived',
  nameSince: 1762400000000,
})

// An entry of another session.
export const ENTRY_OTHER = JSON.stringify({
  pid: 4343,
  sessionId: 'sess-other',
  name: 'someone-else',
  nameSource: 'user',
  nameSince: 1762400000000,
})

// An entry the session later renamed.
export const ENTRY_RENAMED = JSON.stringify({
  pid: 4242,
  sessionId: 'sess-1',
  name: 'renamed',
  nameSource: 'user',
  nameSince: 1762400100000,
  formerNames: ['drain-bead'],
})

// A partially written entry.
export const ENTRY_MALFORMED = '{"pid": 4242, "sessionId": "sess-1", "na'
