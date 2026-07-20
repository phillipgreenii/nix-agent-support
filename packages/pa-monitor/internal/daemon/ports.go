package daemon

import "github.com/phillipgreenii/pa-monitor/internal/core/limits"

// Limits is the current account-global rate_limits reading (ADR 0021 §1). It is
// an alias of limits.Limits: the DTO + the ADR-0029 window-peak fold live in the
// leaf package internal/core/limits, which the corpus Monitor's Limits observer
// folds and the daemon tick reads (via the poller's MonitorLimits). The alias
// keeps applyLimits / blockToStoreBlockWithLimits working unchanged. The old
// LimitsSource port + its SiblingLimitsSource adapter were removed in pg2-66h9g
// (the Monitor is the sole rate_limits reader).
type Limits = limits.Limits
