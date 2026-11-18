package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const probeQuery = `count(last_over_time(up[5m]))`

// promHasSamples asks Prometheus whether any job reported a sample in the last
// five minutes. Any failure (refused, timeout, non-200, empty) means "no
// samples": that is the condition being detected.
func promHasSamples(base string, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	u := base + "/api/v1/query?query=" + url.QueryEscape(probeQuery)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Data struct {
			Result []struct {
				Value []any `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil {
		return false
	}
	for _, r := range body.Data.Result {
		if len(r.Value) == 2 {
			if s, ok := r.Value[1].(string); ok {
				if v, err := strconv.ParseFloat(s, 64); err == nil && v > 0 {
					return true
				}
			}
		}
	}
	return false
}
