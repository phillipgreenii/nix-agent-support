# Gate test disposition list and regression baseline

This document is the regression guard for the conversion of gates into levels of
resources. It has two parts: the baseline record (taken before any code changed)
and the disposition list (one row per gate-related test function).

Rules for later work:

- A test marked **ported** MUST be ported by the implementation work that rewrites
  the code under test, to the level test named in its row (the name is a target,
  not a promise; the package is binding).
- A test marked **dropped** MUST NOT be ported; the reason is binding.
- The ten behavior-carrying tests (marked NAMED) MUST NOT be dropped.
- Goldens are reviewed by hand, never regenerated in bulk.
- This document MUST NOT contain a tracker id or an employer-specific name.

## 1. Baseline record

Taken on 2026-11-10 on the unmodified tree, before any change. Command form:
`nice -n 10 go test -p 4 [-cover] ./...` run in each module directory under
`packages/`. Coverage figures are the per-package statement coverage printed by
the run with `-cover`.

| Module                        | Command                     | Result                |
| ----------------------------- | --------------------------- | --------------------- |
| pg-router                     | `go test -p 4 -cover ./...` | PASS (31 packages ok) |
| pg-router-disk-watchdog       | `go test -p 4 -cover ./...` | PASS                  |
| pg-desk                       | `go test -p 4 -cover ./...` | PASS                  |
| pg-connector                  | `go test -p 4 ./...`        | PASS                  |
| pg-claude-code-recorder       | `go test -p 4 ./...`        | PASS                  |
| claude-extended-tool-approver | `go test -p 4 ./...`        | PASS                  |

Notes:

1. With `-cover`, three modules (pg-connector, pg-claude-code-recorder,
   claude-extended-tool-approver) fail to BUILD a handful of packages that have no
   test files, with `compile: version "go1.26.0" does not match go tool version
"go1.25.9"` (a toolchain mismatch of the coverage-instrumented build on this host).
   This is an environment artifact of `-cover` on test-less packages, not a test
   failure; the same suites pass without `-cover`, which is the form recorded above.
2. The alert rule scripts: `pg-desk/grafana/check-alert-structure.sh`,
   `pg-connector/grafana/check-alert-structure.sh` and
   `pg-claude-code-recorder/grafana/check-alert-structure.sh` PASS (exit 0, with the
   argument lists the flake uses). `pg-router/check-alert-rules.sh` and
   `pg-desk/grafana/check-alert-rules.sh` could NOT be baselined here: they need
   `promtool`, which is not on PATH on this host (exit 127). They are covered by the
   corresponding flake checks; the baseline is "not run, promtool unavailable".

### 1.1 pg-router coverage per touched package (with `-cover`)

| Package               | Statements covered |
| --------------------- | ------------------ |
| cmd/pg-router         | 81.9%              |
| internal/config       | 93.5%              |
| internal/core         | 87.0%              |
| internal/discover     | 93.8%              |
| internal/eventqueue   | 92.5%              |
| internal/metrics      | 90.4%              |
| internal/orchestrator | 96.5%              |
| internal/tui          | 93.5%              |
| internal/tui/render   | 97.2%              |

Other modules (with `-cover`): pg-router-disk-watchdog cmd 87.9%; pg-desk cmd/pg-desk
87.2%.

## 2. Counts check

`rg -c '^func Test'` per file, equal to the design counts:

| File (under packages/pg-router unless noted)                                                                      | Count   |
| ----------------------------------------------------------------------------------------------------------------- | ------- |
| internal/eventqueue/gate_test.go                                                                                  | 19      |
| internal/tui/gates_test.go                                                                                        | 15      |
| internal/core/gates_test.go                                                                                       | 13      |
| cmd/pg-router/gates_cmd_test.go                                                                                   | 9       |
| internal/orchestrator/gates_test.go                                                                               | 6       |
| internal/config/gates_test.go                                                                                     | 5       |
| internal/metrics/gate_metrics_test.go                                                                             | 5       |
| cmd/pg-router/run_gates_test.go                                                                                   | 4       |
| internal/discover/limits_test.go                                                                                  | 4       |
| Subtotal                                                                                                          | 80      |
| packages/pg-router-disk-watchdog/cmd/pg-router-disk-watchdog/pressure_test.go (one row only: the hysteresis test) | 1 of 13 |
| Total rows                                                                                                        | 81      |

## 3. Disposition list

Legend: NAMED marks one of the ten behavior-carrying tests (never droppable).
"admit" means the pure admission decision and explanation package; "levels store"
means the durable level records and evaluation in eventqueue; "registry" means the
resource registry and requires clause compiler in config.

### 3.1 internal/eventqueue/gate_test.go (19)

| #   | Test                                                            | Disposition                                                                                                                                             |
| --- | --------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | TestValidateGateType                                            | ported to a resource-name validation test, package internal/config (registry)                                                                           |
| 2   | TestSetGate_RejectsInvalidType                                  | ported to a level_set-rejects-unknown-resource test, package internal/eventqueue (levels store)                                                         |
| 3   | TestSetGate_LastWriterWinsPerType                               | ported to a per (resource, owner) replacement test with duplicate / conflict / older_than_held outcomes, package internal/eventqueue (levels store)     |
| 4   | TestClearGate_AnyCallerAndIdempotent                            | ported to a level clear idempotence test, package internal/eventqueue (levels store)                                                                    |
| 5   | TestGateRecords_AreAppendedThroughTheStore                      | ported to a level records appended through the store test, package internal/eventqueue (levels store)                                                   |
| 6   | TestGate_TTLLeaseLapsesAndRenews (NAMED)                        | ported to a strict-staleness, lease lapse, renewal and grace test, package internal/eventqueue (levels store)                                           |
| 7   | TestGate_ReplaySurvivesRestartAndHonorsTTL (NAMED)              | ported to a level replay across restart honoring expires_at and lapsed override test, package internal/eventqueue (levels store)                        |
| 8   | TestGate_StateDoesNotDependOnEventRetention                     | ported to a level state independent of event retention test, package internal/eventqueue (levels store)                                                 |
| 9   | TestGate_RecordsAreRoutableEvents                               | ported to a level records routable as events test, package internal/eventqueue (levels store)                                                           |
| 10  | TestGate_NoSubscriberMeansNoRoutedEventButStillLogged           | ported to the same behavior for level records, package internal/eventqueue (levels store)                                                               |
| 11  | TestGate_GateEventsBypassGatingForDelivery                      | ported to level events bypass admission for delivery, package internal/eventqueue (admission)                                                           |
| 12  | TestGate_BlockedListenerIsNotDispatchedUnblockedStillIs (NAMED) | ported to a level-blocked role is not dispatched while a role with no requirement still is, package internal/eventqueue (admission)                     |
| 13  | TestGate_ExemptionIsPerType                                     | ported to a per-resource requires clause test (unmentioned resource means does not care), package internal/eventqueue (admission)                       |
| 14  | TestGate_FinalAttemptLostWhileBlockedAndCounted (NAMED)         | ported to a final attempt lost while level-blocked and counted (expiry at the event's existing expiresAt) test, package internal/eventqueue (admission) |
| 15  | TestGate_PushStillAcceptedAndAckStillProcessed (NAMED)          | ported to push accepted and ack processed while level-blocked, package internal/eventqueue (admission)                                                  |
| 16  | TestGate_CheckPullRejectsBlockedListenerNamingTheGate (NAMED)   | ported to CheckPull rejects a level-blocked listener and the reason names the resource and level, package internal/eventqueue (admission)               |
| 17  | TestGate_EmitterBlockedBy                                       | ported to emitter blocked-by explanation test, package internal/eventqueue (admission)                                                                  |
| 18  | TestGate_GateEventsNeverCountAsUnconsumedMisses                 | ported to level events never count as unconsumed misses, package internal/eventqueue (levels store)                                                     |
| 19  | TestGateRecordsDoNotDisturbEventReplay                          | ported to level records do not disturb event replay (plus legacy gate records ignored on replay), package internal/eventqueue (levels store)            |

### 3.2 internal/tui/gates_test.go (15)

Read-related rows are ported by the core-site work; banner and problems-view rows by
the surface work.

| #   | Test                                               | Disposition                                                                                                                                  |
| --- | -------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | TestToggleOperatorGate_NoOptimisticFlip            | ported to the router PAUSED operator toggle with no optimistic flip, package internal/tui (surface)                                          |
| 2   | TestToggleOperatorGate_UsesResumeWhenAlreadyPaused | ported to toggle uses resume when the router level is PAUSED, package internal/tui (surface)                                                 |
| 3   | TestToggleOperatorGate_IgnoresOtherGates           | ported to toggle ignores non-router resources, package internal/tui (surface)                                                                |
| 4   | TestToggleOperatorGate_HandleNeverReachesRawClient | ported to the same handle-only wiring test, package internal/tui (surface)                                                                   |
| 5   | TestGateToggle_FailureFlash                        | ported to level toggle failure flash, package internal/tui (surface)                                                                         |
| 6   | TestGateToggle_SuccessFlashNamesEffectiveAggregate | ported to success flash names the effective levels, package internal/tui (surface)                                                           |
| 7   | TestGateToggle_ResumedFlashWhenNothingElseIsActive | ported to resumed flash when no level is blocking, package internal/tui (surface)                                                            |
| 8   | TestResumeAllGates_NoopOutsideGatesModal           | ported to clear-all no-op outside the levels modal, package internal/tui (surface)                                                           |
| 9   | TestResumeAllGates_ClearsEveryGateInsideGatesModal | ported to clear-all inside the levels modal clears every operator override, package internal/tui (surface)                                   |
| 10  | TestRenderGatesModal_ListsEveryActiveGate          | ported to the levels modal lists every resource reading and override, package internal/tui (surface)                                         |
| 11  | TestRenderGatesModal_NoneActiveIsUnambiguous       | ported to the levels modal with nothing blocking is unambiguous, package internal/tui (surface)                                              |
| 12  | TestRenderGatesModal_LongTypeGetsGuaranteedGap     | ported to long resource name keeps a guaranteed gap, package internal/tui (surface)                                                          |
| 13  | TestAsOfRaceGuard                                  | ported to the as-of race guard on the levels read, package internal/tui (read, core-site)                                                    |
| 14  | TestApplyPollResult_PreservesOpenModalScreen       | ported to poll result preserves the open modal with levels data, package internal/tui (read, core-site)                                      |
| 15  | TestGateDetail_CarriesEveryField                   | ported to level detail carries every field (resource, level, owner, observed_at, expires_at, reason), package internal/tui (read, core-site) |

### 3.3 internal/core/gates_test.go (13)

| #   | Test                                                   | Disposition                                                                                                                   |
| --- | ------------------------------------------------------ | ----------------------------------------------------------------------------------------------------------------------------- |
| 1   | TestGateSet_SetsRecordsAndRenews                       | ported to level set records and renews (operator override), package internal/core                                             |
| 2   | TestGateSet_RejectsBadRequests                         | ported to level set rejects bad requests, package internal/core                                                               |
| 3   | TestGateClear_AnyCallerAndIdempotent                   | ported to override clear any caller and idempotent, package internal/core                                                     |
| 4   | TestGateClear_AllAndSelectorValidation                 | ported to clear all and selector validation, package internal/core                                                            |
| 5   | TestPause_SetsSystemPauseWithOperatorAsOwner           | ported to pause sets router PAUSED with the operator as owner, package internal/core                                          |
| 6   | TestResume_ClearsOnlySystemPauseUnlessAll              | ported to resume clears only the router PAUSED operator record unless all, package internal/core                              |
| 7   | TestStatus_ReportsActiveGatesAndHonorsLeases           | ported to status reports levels and honors leases, package internal/core                                                      |
| 8   | TestAdmitPull_HonorsRegistrationTimeExemptions (NAMED) | ported to AdmitPull honors registration-time requires clauses, package internal/core                                          |
| 9   | TestRegister_RejectsNonCapsNonBlockingGate             | ported to registration rejects an unknown or malformed requires clause, package internal/core (with internal/config registry) |
| 10  | TestGateVerbs_ConcurrentChurn (NAMED)                  | ported to level verbs concurrent churn under the race detector, package internal/core                                         |
| 11  | TestGateVerbs_RejectMalformedRequests                  | ported to level verbs reject malformed requests, package internal/core                                                        |
| 12  | TestGateVerbs_StoreFailureLeavesGatesUntouched         | ported to store failure leaves levels untouched, package internal/core                                                        |
| 13  | TestGateClear_StoreFailureKeepsTheGate                 | ported to clear store failure keeps the override, package internal/core                                                       |

### 3.4 cmd/pg-router/gates_cmd_test.go (9)

| #   | Test                                              | Disposition                                                                                                                                                                                        |
| --- | ------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | TestPauseCore_SetsSystemPauseRecordingTheOperator | ported to pause core records the operator as owner of router PAUSED, package cmd/pg-router                                                                                                         |
| 2   | TestResumeCore_ClearsSystemPauseOnlyUnlessAll     | ported to resume core clears only the router operator record unless all, package cmd/pg-router                                                                                                     |
| 3   | TestGateSetCore_DescriptionOwnerTTLAndRenewal     | ported to level set command with reason, owner, lease and renewal, package cmd/pg-router                                                                                                           |
| 4   | TestGateClearCore_OneAllAndIdempotent             | ported to level clear command one / all / idempotent, package cmd/pg-router                                                                                                                        |
| 5   | TestGateListCore_TextAndJSON                      | ported to level list text and JSON output, package cmd/pg-router                                                                                                                                   |
| 6   | TestGateCommands_NoRunningCoreIsExit1             | ported to level commands with no running core exit 1, package cmd/pg-router                                                                                                                        |
| 7   | TestRunGate_UsageErrors                           | ported to level command usage errors, package cmd/pg-router                                                                                                                                        |
| 8   | TestRunPauseResume_PositionalGateNameIsUsageError | ported to pause / resume positional resource name is a usage error, package cmd/pg-router                                                                                                          |
| 9   | TestShortTime                                     | ported to the same short time helper test if the helper survives; otherwise dropped with the helper (decided by the conversion work, which MUST record which in its change), package cmd/pg-router |

### 3.5 internal/orchestrator/gates_test.go (6)

| #   | Test                                                             | Disposition                                                                                                           |
| --- | ---------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| 1   | TestProduceTick_gatedEmittersAreNotPolled_exemptAndTimerStillRun | ported to level-blocked emitters are not polled while sensors and the timer still run, package internal/orchestrator  |
| 2   | TestDispatch_gatedListenerBlockedExemptListenerRuns              | ported to level-blocked listener blocked and a listener with no requirement runs, package internal/orchestrator       |
| 3   | TestRoleListener_declaresRoleNonBlockingGates                    | ported to a role listener declares its requires clause, package internal/orchestrator                                 |
| 4   | TestDispatch_diskWatchdogStillRunsWhileLowDiskUsageIsActive      | ported to a registration with no disk requirement still runs at low disk, package internal/orchestrator               |
| 5   | TestDispatch_beadServerDownGateRunsOnlyListenersThatExemptIt     | ported to a tracker-down level runs only registrations that do not require the tracker, package internal/orchestrator |
| 6   | TestProduceTick_beadServerDownGateOnlyPollsEmittersThatExemptIt  | ported to a tracker-down level polls only emitters that do not require the tracker, package internal/orchestrator     |

### 3.6 internal/config/gates_test.go (5)

| #   | Test                                             | Disposition                                                                                                                                                   |
| --- | ------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 1   | TestLoad_nonBlockingGatesOnRoleAndQuery          | ported to requires clause load on role and query, package internal/config (registry)                                                                          |
| 2   | TestLoad_nonBlockingGatesMustBeAllCaps           | dropped: the all-caps gate-type naming rule disappears with gate types; resource and level names are validated by the registry name test (row 3.1 #1) instead |
| 3   | TestLoad_timerQueryTypeIsTheGateImmuneEmitter    | ported to the timer query type is level-immune, package internal/config (registry)                                                                            |
| 4   | TestValidate_gateEventTypesAreNotOrphanConsumers | ported to level event types are not orphan consumers, package internal/config (registry)                                                                      |
| 5   | TestLoad_retiredGatePathKeysAreIgnored           | ported to retired configuration keys are ignored, package internal/config (registry)                                                                          |

### 3.7 internal/metrics/gate_metrics_test.go (5)

| #   | Test                                                                    | Disposition                                                                                                     |
| --- | ----------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| 1   | TestGateMetrics_ActiveGaugeAndSetClearExpireCounters                    | ported to level gauge and set / clear / expire counters, package internal/metrics                               |
| 2   | TestGateMetrics_BlockedAndDropsPerParticipantAndType_OwnerIsNeverALabel | ported to blocked and drop counters per participant and resource, owner never a label, package internal/metrics |
| 3   | TestGateMetrics_TypeCardinalityIsCapped                                 | ported to resource cardinality capped, package internal/metrics                                                 |
| 4   | TestQueueLogBytesGauge                                                  | ported unchanged (queue log gauge is not gate specific but lives in this file), package internal/metrics        |
| 5   | TestGateMetrics_ActiveGaugeFollowsRenewalLapseAndClearOnFileStore       | ported to level gauge follows renewal, lapse and clear on the file store, package internal/metrics              |

### 3.8 cmd/pg-router/run_gates_test.go (4)

| #   | Test                                                               | Disposition                                                                                     |
| --- | ------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------- |
| 1   | TestRunUntilIdleBody_gatedReachableAnswersIngestNoDispatch (NAMED) | ported to level-blocked: reachable answers ingest and nothing dispatches, package cmd/pg-router |
| 2   | TestRunOneTick_expiryContinuesWhileGated                           | ported to expiry continues while level-blocked, package cmd/pg-router                           |
| 3   | TestRunOneTick_gateNoticeOncePerChange                             | ported to level notice logged once per change, package cmd/pg-router                            |
| 4   | TestGateNotice_formatsEveryGate                                    | ported to level notice formats every blocking level, package cmd/pg-router                      |

### 3.9 internal/discover/limits_test.go (4)

| #   | Test                                                       | Disposition                                                                                       |
| --- | ---------------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| 1   | TestProduce_emittersHaltedSkipsPolledSourcesButNotTimer    | ported to level-blocked emitters skip polled sources but not the timer, package internal/discover |
| 2   | TestProduce_emittersHaltedSkipsThresholdSource             | ported to level-blocked emitters skip the threshold source, package internal/discover             |
| 3   | TestProduce_hardLimitRefusalIsNonFatalAndIsolatedPerSource | ported unchanged (hard limit refusal is not gate specific), package internal/discover             |
| 4   | TestProduce_unclassifiedEnqueueFailureStillAbortsThePass   | ported unchanged (not gate specific), package internal/discover                                   |

### 3.10 packages/pg-router-disk-watchdog

| #   | Test                                                                                        | Disposition                                                                                                                                            |
| --- | ------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | TestPressure_loadRecoveryHysteresis (NAMED) in cmd/pg-router-disk-watchdog/pressure_test.go | ported: moves to the sensors module library (the watchdog package is deleted; load hysteresis becomes a sensor concern), package of the sensors module |

## 4. Summary

- Rows: 19 + 15 + 13 + 9 + 6 + 5 + 5 + 4 + 4 = 80, plus 1 = 81.
- Dropped: 1 (config all-caps naming rule). TestShortTime is conditional on the helper surviving.
- All ten NAMED tests are marked ported.
