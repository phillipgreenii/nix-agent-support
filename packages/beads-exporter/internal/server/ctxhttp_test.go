package server

import (
	"io"
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

// httpPost issues a POST bound to the test's context.
func httpPost(t testing.TB, url, contentType string, body io.Reader) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	return http.DefaultClient.Do(req)
}
