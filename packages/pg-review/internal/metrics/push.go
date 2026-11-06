package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PushTimeout bounds one whole push (the OTLP request, or the ccpool fallback
// for all gauges together).
const PushTimeout = 2 * time.Second

// DebugEnv, set to 1, makes a failed push visible on standard error.
const DebugEnv = "PG_REVIEW_DEBUG"

// Pusher pushes gauges best effort. Its fields are test seams; NewPusher fills
// them with the real thing.
type Pusher struct {
	Getenv   func(string) string
	Client   *http.Client
	LookPath func(string) (string, error)
	// RunCmd runs an external command under ctx with the process environment.
	RunCmd  func(ctx context.Context, name string, args ...string) error
	Debug   io.Writer
	Timeout time.Duration
}

// Default is the pusher Emit uses; tests replace it.
var Default = NewPusher()

// NewPusher returns a pusher over the real environment, HTTP client and PATH.
func NewPusher() *Pusher {
	return &Pusher{
		Getenv:   os.Getenv,
		Client:   &http.Client{},
		LookPath: exec.LookPath,
		RunCmd: func(ctx context.Context, name string, args ...string) error {
			return exec.CommandContext(ctx, name, args...).Run()
		},
		Debug:   os.Stderr,
		Timeout: PushTimeout,
	}
}

func (p *Pusher) debugf(format string, a ...any) {
	if p.Getenv(DebugEnv) != "1" || p.Debug == nil {
		return
	}
	fmt.Fprintf(p.Debug, "pg-review: metrics: "+format+"\n", a...)
}

// Push sends the gauges. With no OTLP endpoint configured it does nothing and
// says nothing. A failed push is logged at debug level; the ccpool emit-gauge
// fallback is tried only when the OTLP push failed and ccpool is on PATH. Push
// never returns an error and never takes longer than the timeout.
func (p *Pusher) Push(ctx context.Context, gauges []Gauge) {
	defer func() {
		if r := recover(); r != nil {
			p.debugf("push panicked: %v", r)
		}
	}()
	if len(gauges) == 0 {
		return
	}
	endpoint := p.firstEnv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	err := p.pushOTLP(ctx, endpoint, gauges)
	if err == nil {
		return
	}
	p.debugf("OTLP push failed: %v", err)
	if _, lerr := p.LookPath("ccpool"); lerr != nil {
		return
	}
	if ferr := p.pushCcpool(ctx, gauges); ferr != nil {
		p.debugf("ccpool emit-gauge fallback failed: %v", ferr)
	}
}

func (p *Pusher) firstEnv(names ...string) string {
	for _, n := range names {
		if v := p.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// pushOTLP posts OTLP/HTTP JSON. The gRPC protocol is not spoken natively; it
// is reported as a failure so the ccpool fallback carries the gauges.
func (p *Pusher) pushOTLP(ctx context.Context, endpoint string, gauges []Gauge) error {
	proto := strings.ToLower(p.firstEnv("OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "OTEL_EXPORTER_OTLP_PROTOCOL"))
	if proto == "grpc" {
		return fmt.Errorf("protocol grpc is not spoken natively")
	}
	url := endpoint
	if p.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") == "" {
		url = strings.TrimRight(endpoint, "/") + "/v1/metrics"
	}
	body, err := otlpJSON(p.firstEnv("OTEL_SERVICE_NAME"), gauges, time.Now())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("endpoint answered %s", resp.Status)
	}
	return nil
}

func (p *Pusher) pushCcpool(ctx context.Context, gauges []Gauge) error {
	for _, gg := range gauges {
		args := []string{"emit-gauge", "--name", gg.Name, "--value", strconv.FormatFloat(gg.Value, 'g', -1, 64)}
		keys := make([]string, 0, len(gg.Labels))
		for k := range gg.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			args = append(args, "--label", k+"="+gg.Labels[k])
		}
		if err := p.RunCmd(ctx, "ccpool", args...); err != nil {
			return err
		}
	}
	return nil
}

type kv struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

func attr(k, v string) kv { return kv{Key: k, Value: map[string]any{"stringValue": v}} }

func otlpJSON(service string, gauges []Gauge, now time.Time) ([]byte, error) {
	if service == "" {
		service = "pg-review"
	}
	byName := map[string][]map[string]any{}
	var order []string
	ts := strconv.FormatInt(now.UnixNano(), 10)
	for _, gg := range gauges {
		if _, ok := byName[gg.Name]; !ok {
			order = append(order, gg.Name)
		}
		keys := make([]string, 0, len(gg.Labels))
		for k := range gg.Labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		attrs := make([]kv, 0, len(keys))
		for _, k := range keys {
			attrs = append(attrs, attr(k, gg.Labels[k]))
		}
		byName[gg.Name] = append(byName[gg.Name], map[string]any{
			"attributes": attrs, "timeUnixNano": ts, "asDouble": gg.Value,
		})
	}
	metrics := make([]map[string]any, 0, len(order))
	for _, n := range order {
		metrics = append(metrics, map[string]any{"name": n, "gauge": map[string]any{"dataPoints": byName[n]}})
	}
	doc := map[string]any{"resourceMetrics": []any{map[string]any{
		"resource":     map[string]any{"attributes": []kv{attr("service.name", service)}},
		"scopeMetrics": []any{map[string]any{"scope": map[string]any{"name": "pg-review"}, "metrics": metrics}},
	}}}
	return json.Marshal(doc)
}
