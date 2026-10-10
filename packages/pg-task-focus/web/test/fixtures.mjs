// Documents shaped like the daemon's (api/openapi.yaml), for the view tests.
// The end-to-end test reads the real ones from a real daemon.

export const NY = "America/New_York";
export const T0 = Date.parse("2026-10-07T13:00:00.000Z"); // 09:00 in New York

export function iso(ms) {
  return new Date(ms).toISOString();
}

export function task(over = {}) {
  return {
    id: "day:2026-10-07:post-plan",
    kind: "day",
    definition: "post-plan",
    cadence: "daily",
    period_start: "2026-10-07",
    title: "Post the plan for the day",
    group: "Start of day",
    due: "2026-10-07T13:30:00.000Z",
    due_zone: NY,
    status: "open",
    overdue: false,
    ...over,
  };
}

export function cycle(over = {}) {
  return {
    id: "01JC0000000000000000000001",
    type: "deep-work",
    title: "Deep work cycle",
    planned_minutes: 50,
    boost_minutes: 0,
    status: "running",
    started_at: "2026-10-07T13:00:00.000Z",
    running_since: "2026-10-07T13:00:00.000Z",
    elapsed_seconds: 600,
    remaining_seconds: 2400,
    overtime: false,
    ends_at: "2026-10-07T13:50:00.000Z",
    not_in_profile: false,
    kv: [],
    alert: { sound: "Hero", reminder_sound: "Hero", repeat_minutes: 10 },
    ...over,
  };
}

export function dimmed(over = {}) {
  return { ...cycle({ id: "01JC0000000000000000000002", type: "review", title: "Review cycle", status: "paused", elapsed_seconds: 720, remaining_seconds: 780, planned_minutes: 25, running_since: undefined, ends_at: undefined }), can_switch: true, ...over };
}

export function period(kind, over = {}) {
  const base = {
    day: { kind: "day", start: "2026-10-07", end: "2026-10-07" },
    week: { kind: "week", start: "2026-10-05", end: "2026-10-11" },
    sprint: { kind: "sprint", start: "2026-10-05", end: "2026-10-18" },
  }[kind];
  return { ...base, zone: NY, today: "2026-10-07", ended: false, ...over };
}

export function state(over = {}) {
  return {
    read_at: iso(T0),
    initialized: true,
    profile: "normal",
    version: { log_lines: 40, config_generation: 1 },
    store: { state: "ok" },
    periods: [period("day"), period("week"), period("sprint")],
    tasks: [
      task({ id: "day:2026-10-07:plan-day", definition: "plan-day", title: "Plan the day", due: "2026-10-07T13:00:00.000Z" }),
      task(),
      task({ id: "week:2026-10-05:weekly-update", kind: "week", cadence: "weekly", period_start: "2026-10-05", definition: "weekly-update", title: "Write the weekly status update", group: undefined, due: "2026-10-08T13:00:00.000Z" }),
      task({ id: "sprint:2026-10-05:capacity-check", kind: "sprint", cadence: "sprint", period_start: "2026-10-05", definition: "capacity-check", title: "Do the capacity check", group: undefined, due: "2026-10-05T13:00:00.000Z", overdue: true }),
    ],
    next: null,
    focus: null,
    dimmed: [],
    interrupt_stack: [],
    resume_offer: null,
    ...over,
  };
}

export const config = {
  digest: "abc",
  listen_port: 49210,
  defaults: {
    cycle_minutes: 25,
    boost_minutes: [5, 10, 25],
    profile: "normal",
    max_future_skew_seconds: 60,
    alert: { sound: "Glass", reminder_sound: "Tink", repeat_minutes: 5 },
    attention: { due_soon_minutes: 30, overtime_high_minutes: 15, stale_pause_minutes: 45 },
  },
  group_order: ["Start of day", "End of day"],
  profiles: [
    { name: "normal", daily: ["plan-day", "post-plan"], weekly: ["weekly-update"], sprint: ["capacity-check"], cycles: ["notifications", "review", "deep-work"] },
    { name: "on-call", daily: ["plan-day"], weekly: [], sprint: [], cycles: ["notifications", "review", "page-response"] },
  ],
  tasks: [],
  cycles: [
    { id: "notifications", title: "Notification cycle", minutes: 25, keys: [], alert: { sound: "Glass", reminder_sound: "Tink", repeat_minutes: 5 } },
    { id: "review", title: "Review cycle", minutes: 25, keys: ["pr"], alert: { sound: "Glass", reminder_sound: "Tink", repeat_minutes: 5 } },
    { id: "page-response", title: "Page response", minutes: 25, keys: ["incident"], alert: { sound: "Glass", reminder_sound: "Tink", repeat_minutes: 5 } },
    { id: "deep-work", title: "Deep work cycle", minutes: 50, keys: ["ticket", "pr"], alert: { sound: "Hero", reminder_sound: "Hero", repeat_minutes: 10 } },
  ],
};

/** A model as the store keeps it, built around a state, for the view tests. */
export function model(over = {}) {
  return {
    now: T0,
    browserZone: NY,
    route: { name: "today" },
    conn: "live",
    loaded: true,
    loadError: null,
    state: state(),
    receivedAt: T0,
    config,
    configGeneration: 1,
    busy: 0,
    notice: null,
    error: null,
    undo: null,
    announce: { polite: { seq: 0, items: [] }, assertive: { seq: 0, items: [] } },
    overtimeAnnounced: {},
    ui: {},
    ...over,
  };
}

export const readOnlyStore = { state: "read_only", reason: "the append fsync failed", since: "2026-10-07T13:00:00.000Z" };
