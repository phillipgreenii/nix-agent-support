package ccusage

import "time"

type BurnRate struct {
	TokensPerMinute float64 `json:"tokensPerMinute"`
	CostPerHour     float64 `json:"costPerHour"`
}

type Projection struct {
	TotalCost        float64 `json:"totalCost"`
	RemainingMinutes int     `json:"remainingMinutes"`
}

type Block struct {
	ID         string     `json:"id"`
	StartTime  time.Time  `json:"startTime"`
	EndTime    time.Time  `json:"endTime"`
	IsActive   bool       `json:"isActive"`
	CostUSD    float64    `json:"costUSD"`
	BurnRate   BurnRate   `json:"burnRate"`
	Projection Projection `json:"projection"`
}

type BlocksResponse struct {
	Blocks []Block `json:"blocks"`
}

// WeeklyEntry mirrors one row from `ccusage weekly --json --offline`.
// Period is the Monday of the week in YYYY-MM-DD form, local time.
type WeeklyEntry struct {
	Period    string  `json:"period"`
	TotalCost float64 `json:"totalCost"`
	Agent     string  `json:"agent"`
}

type WeeklyResponse struct {
	Weekly []WeeklyEntry `json:"weekly"`
	Totals struct {
		TotalCost float64 `json:"totalCost"`
	} `json:"totals"`
}
