package wire

// Health is the document /healthz and /readyz return. Status is "starting"
// until replay has finished, "ok" while the service is ready and nothing is
// wrong, and "degraded" while it is ready but the store is read-only, the last
// reload failed or the store is not writable.
type Health struct {
	Status        string        `json:"status"`
	Ready         bool          `json:"ready"`
	FailingCheck  string        `json:"failing_check,omitempty"`
	Version       string        `json:"version"`
	SchemaVersion int           `json:"schema_version"`
	UptimeSeconds float64       `json:"uptime_seconds"`
	Replay        HealthReplay  `json:"replay"`
	LastAppend    *Instant      `json:"last_append"`
	Store         HealthStore   `json:"store"`
	Config        HealthConfig  `json:"config"`
	Recovery      HealthRecover `json:"recovery"`
	Alerts        HealthAlerts  `json:"alerts"`
}

// HealthReplay is what the startup replay did.
type HealthReplay struct {
	Events          int     `json:"events"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// HealthStore is the store's health with its writability probe and size.
// Reason and Since are present only while the store is read-only.
type HealthStore struct {
	State     string   `json:"state"`
	Reason    string   `json:"reason,omitempty"`
	Since     *Instant `json:"since,omitempty"`
	Writable  bool     `json:"writable"`
	SizeBytes int64    `json:"size_bytes"`
}

// HealthConfig is the state of the configuration. ReloadError names the
// problems of the last reload by path and rule; it never echoes a configured
// value. RestartRequired lists the settings a reload saw change that take
// effect only on restart.
type HealthConfig struct {
	Valid           bool     `json:"valid"`
	Digest          string   `json:"digest"`
	LoadedAt        *Instant `json:"loaded_at"`
	ReloadError     string   `json:"reload_error,omitempty"`
	RestartRequired []string `json:"restart_required"`
}

// HealthRecover is what startup recovery did to the end of the log.
type HealthRecover struct {
	TornTail           bool `json:"torn_tail"`
	UncommittedBatches int  `json:"uncommitted_batches"`
}

// HealthAlerts is the alert scheduler's reading: the running cycle and when
// its next reminder falls due if nothing changes.
type HealthAlerts struct {
	RunningCycle string   `json:"running_cycle,omitempty"`
	NextReminder *Instant `json:"next_reminder"`
}
