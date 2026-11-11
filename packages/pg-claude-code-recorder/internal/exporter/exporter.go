// Package exporter serves the recorder's registry in the Prometheus text
// exposition format on GET /metrics, the same shape the other workspace
// daemons expose. Every series name carries the pg_claude_code_recorder_
// prefix; labels are the bounded ones the registry holds (source, type, reason,
// stage, kind, client), never a per-file or per-session label.
package exporter

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-claude-code-recorder/internal/obs"
)

// Prefix is prepended to every exported series name.
const Prefix = "pg_claude_code_recorder_"

// splitKey splits a registry key "name{a=b,c=d}" into the name and the labels
// rendered as `a="b",c="d"`.
func splitKey(k string) (name, labels string) {
	i := strings.IndexByte(k, '{')
	if i < 0 {
		return k, ""
	}
	name = k[:i]
	body := strings.TrimSuffix(k[i+1:], "}")
	var parts []string
	for _, kv := range strings.Split(body, ",") {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		parts = append(parts, kv[:eq]+"="+strconv.Quote(kv[eq+1:]))
	}
	return name, strings.Join(parts, ",")
}

func series(name, labels string) string {
	if labels == "" {
		return Prefix + name
	}
	return Prefix + name + "{" + labels + "}"
}

// helpLine writes the # HELP line of a metric when it has help text.
func helpLine(w io.Writer, help map[string]string, name string) {
	if text, ok := help[name]; ok {
		text = strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(text)
		_, _ = fmt.Fprintf(w, "# HELP %s%s %s\n", Prefix, name, text)
	}
}

// Render writes reg in the text exposition format, series sorted for a stable
// output. A metric with help text gets a # HELP line before its # TYPE line.
func Render(w io.Writer, reg *obs.Registry) {
	counters, gauges, hists := reg.Snapshot()
	help := reg.HelpTexts()
	write := func(kind string, m map[string]float64) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		typed := map[string]bool{}
		for _, k := range keys {
			name, labels := splitKey(k)
			if !typed[name] {
				typed[name] = true
				helpLine(w, help, name)
				_, _ = fmt.Fprintf(w, "# TYPE %s%s %s\n", Prefix, name, kind)
			}
			_, _ = fmt.Fprintf(w, "%s %s\n", series(name, labels), strconv.FormatFloat(m[k], 'g', -1, 64))
		}
	}
	write("counter", counters)
	write("gauge", gauges)
	// Group the keyed histogram series by metric name so each family is typed once.
	byName := map[string][]string{}
	for k := range hists {
		name, _ := splitKey(k)
		byName[name] = append(byName[name], k)
	}
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		helpLine(w, help, n)
		_, _ = fmt.Fprintf(w, "# TYPE %s%s histogram\n", Prefix, n)
		keys := byName[n]
		sort.Strings(keys)
		for _, k := range keys {
			_, labels := splitKey(k)
			h := hists[k]
			// le goes after the series' own labels; _sum and _count carry only those.
			own, lead := "", ""
			if labels != "" {
				own = "{" + labels + "}"
				lead = labels + ","
			}
			var cum uint64
			for i, b := range h.Bounds {
				cum += h.Counts[i]
				_, _ = fmt.Fprintf(w, "%s%s_bucket{%sle=\"%s\"} %d\n", Prefix, n, lead, strconv.FormatFloat(b, 'g', -1, 64), cum)
			}
			cum += h.Counts[len(h.Bounds)]
			_, _ = fmt.Fprintf(w, "%s%s_bucket{%sle=\"+Inf\"} %d\n", Prefix, n, lead, cum)
			_, _ = fmt.Fprintf(w, "%s%s_sum%s %s\n", Prefix, n, own, strconv.FormatFloat(h.Sum, 'g', -1, 64))
			_, _ = fmt.Fprintf(w, "%s%s_count%s %d\n", Prefix, n, own, h.Samples)
		}
	}
}

// Handler serves GET /metrics from reg; refresh, when non-nil, runs before each
// scrape so gauges are current.
func Handler(reg *obs.Registry, refresh func()) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		if refresh != nil {
			refresh()
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		Render(w, reg)
	})
	return mux
}

// Server is a running exporter.
type Server struct {
	srv *http.Server
	ln  net.Listener
}

// Start listens on addr (host:port, loopback only) and serves reg.
func Start(addr string, reg *obs.Registry, refresh func()) (*Server, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("metrics address %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("metrics address %q: only a loopback host is accepted", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &Server{srv: &http.Server{Handler: Handler(reg, refresh), ReadHeaderTimeout: 5 * time.Second}, ln: ln}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr returns the bound address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the exporter: a graceful Shutdown for up to a second, then a hard
// Close of whatever is still open. The wait runs inside ctx, so a cancelled ctx
// goes straight to the hard close.
func (s *Server) Close(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := s.srv.Shutdown(ctx); err != nil {
		// A scrape that outlived the grace window: drop its connection now, so
		// shutdown does not leave a connection goroutine behind.
		_ = s.srv.Close()
	}
}
