package usage

import "time"

// BurnRate is the current spend velocity of a block. Populated by the daemon's
// own periodic sampling (ADR 0021 §1); the native pricer leaves it zero, and
// the display layer omits the projection when burn is zero.
type BurnRate struct {
	TokensPerMinute float64 `json:"tokensPerMinute"`
	CostPerHour     float64 `json:"costPerHour"`
}

// Projection is a block's forecast to the plan cap. Same provenance as BurnRate.
type Projection struct {
	TotalCost        float64 `json:"totalCost"`
	RemainingMinutes int     `json:"remainingMinutes"`
}

// Block is the neutral 5h-block cost DTO consumed across aggregate / proto /
// store / trackers (renamed from ccusage.Block in Phase 4). The native
// CostPricer produces it from transcript usage × config prices.
type Block struct {
	ID         string     `json:"id"`
	StartTime  time.Time  `json:"startTime"`
	EndTime    time.Time  `json:"endTime"`
	IsActive   bool       `json:"isActive"`
	CostUSD    float64    `json:"costUSD"`
	BurnRate   BurnRate   `json:"burnRate"`
	Projection Projection `json:"projection"`
	CapHitAt   *time.Time `json:"capHitAt,omitempty"`
}

// WeeklyEntry is the neutral current-week cost DTO. Period is the Monday of the
// week in YYYY-MM-DD form.
type WeeklyEntry struct {
	Period    string     `json:"period"`
	TotalCost float64    `json:"totalCost"`
	Agent     string     `json:"agent"`
	CapHitAt  *time.Time `json:"capHitAt,omitempty"`
}
