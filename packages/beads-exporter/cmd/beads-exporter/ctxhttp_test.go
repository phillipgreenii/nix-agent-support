package main

import (
	"net/http"
	"testing"
)

// httpGet issues a GET bound to the test's context; a nil client means
// http.DefaultClient.
func httpGet(t testing.TB, c *http.Client, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c == nil {
		c = http.DefaultClient
	}
	return c.Do(req)
}
