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
