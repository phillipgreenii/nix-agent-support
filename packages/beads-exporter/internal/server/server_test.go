package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/beads-exporter/internal/bd"
	"github.com/phillipgreenii/beads-exporter/internal/collect"
	"github.com/phillipgreenii/beads-exporter/internal/metrics"
)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }

type okAdapter struct{}

func (okAdapter) List(context.Context, bd.ListOpts) ([]bd.Bead, error) {
	created := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	return []bd.Bead{{ID: "alpha-1", Status: "open", IssueType: "task", CreatedAt: &created}}, nil
}
func (okAdapter) Ready(context.Context, []string) ([]bd.Bead, error)    { return nil, nil }
func (okAdapter) Blocked(context.Context) ([]bd.Bead, error)            { return nil, nil }
func (okAdapter) CountByStatus(context.Context) (map[string]int, error) { return map[string]int{}, nil }

func (okAdapter) Statuses(context.Context) ([]string, error) { return []string{"open", "closed"}, nil }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestMetricsEndpointServesTheSnapshot(t *testing.T) {
	mp := collect.NewMainPass(nil, 3)
	c := collect.New(fixedClock{}, []collect.DB{{Name: "alpha", Adapter: okAdapter{}}}, []collect.Pass{mp}, quiet())
	c.RunPass(context.Background(), collect.PassMain)

	srv := httptest.NewServer(Handler(c, metrics.Default(), quiet()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != ContentType {
		t.Fatalf("Content-Type = %q", got)
	}
	for _, want := range []string{
		"# TYPE beads_issues gauge",
		`beads_issues{db="alpha",state="other"} 1`,
		`beads_exporter_up{db="alpha"} 1`,
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("body lacks %q:\n%s", want, body)
		}
	}
}

func TestEmptySnapshotIsAnEmptyBody(t *testing.T) {
	c := collect.New(fixedClock{}, nil, nil, quiet())
	srv := httptest.NewServer(Handler(c, metrics.Default(), quiet()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || len(body) != 0 {
		t.Fatalf("status %d body %q", resp.StatusCode, body)
	}
}

func TestOtherPathsAndMethodsAreRejected(t *testing.T) {
	c := collect.New(fixedClock{}, nil, nil, quiet())
	srv := httptest.NewServer(Handler(c, metrics.Default(), quiet()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/other status = %d", resp.StatusCode)
	}
	resp, err = http.Post(srv.URL+"/metrics", "text/plain", strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", resp.StatusCode)
	}
}

func TestRenderErrorIs500(t *testing.T) {
	mp := collect.NewMainPass(nil, 3)
	c := collect.New(fixedClock{}, []collect.DB{{Name: "alpha", Adapter: okAdapter{}}}, []collect.Pass{mp}, quiet())
	c.RunPass(context.Background(), collect.PassMain)
	// A registry that does not know the exporter's families cannot render them.
	empty, _ := metrics.NewRegistry()
	srv := httptest.NewServer(Handler(c, empty, quiet()))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
}
