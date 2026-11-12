// Package netprobe is the HTTPS reachability probe shared by the network
// resource of host-sensor and by vpn-sensor.
//
// A target is reachable when an HTTPS HEAD completes with TLS verification and
// a status below 500 (a 3xx is reachable: redirects are NOT followed). A DNS,
// connect, TLS or timeout failure is unreachable, so a captive portal that
// answers with a certificate for another name fails the check. The resource is
// UNAVAILABLE only when ALL targets fail on Threshold consecutive passes (one
// pass is one sensor invocation); the counter lives in the state file.
//
// A Prober with SkipVerify set instead measures bare reachability: the TCP
// connect, the TLS handshake and an HTTP response must complete, but the
// certificate chain and name are not verified (reachability is not trust).
// Only vpn-sensor uses it, for its reachability resource; the network
// resource of host-sensor always verifies.
//
// Exported names: Prober, Result, Probe, Observe, Defaults (DefaultTargets,
// DefaultTimeout, DefaultThreshold), HostURLs, ConfigureCommon, FailKey.
package netprobe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phillipgreenii/pg-router-sensors/internal/levels"
	"github.com/phillipgreenii/pg-router-sensors/internal/sensorcli"
)

// Defaults (strawman, all configurable).
const (
	DefaultTimeout   = 5 * time.Second
	DefaultThreshold = 3
	// FailKey is the extra-state key holding the consecutive all-fail passes.
	FailKey = "net_failures"
)

// DefaultTargets are the generic public targets of host-sensor's network
// resource.
var DefaultTargets = []string{"https://example.com", "https://example.org", "https://example.net"}

// Prober probes a list of URLs.
type Prober struct {
	// Client is the HTTP client (nil: a default client). Its redirect policy is
	// overridden so a 3xx is returned as is; TLS verification is whatever the
	// client's transport does (the default verifies).
	Client  *http.Client
	Timeout time.Duration
	// SkipVerify skips TLS certificate verification for this prober only (the
	// handshake must still complete). The zero value verifies.
	SkipVerify bool
}

// Result is the outcome of one target.
type Result struct {
	URL       string
	Reachable bool
	Status    int
	Err       error
}

// Probe HEADs every target in parallel and returns the results in target
// order.
func (p *Prober) Probe(ctx context.Context, targets []string) []Result {
	client := http.DefaultClient
	if p.Client != nil {
		client = p.Client
	}
	c := *client
	if p.SkipVerify {
		c.Transport = insecureTransport(client.Transport)
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	out := make([]Result, len(targets))
	var wg sync.WaitGroup
	for i, u := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = headOne(ctx, &c, u, timeout)
		}()
	}
	wg.Wait()
	return out
}

// insecureTransport returns a copy of base (the default transport when base is
// nil or not an *http.Transport) that does not verify server certificates.
func insecureTransport(base http.RoundTripper) *http.Transport {
	t, ok := base.(*http.Transport)
	if !ok || t == nil {
		t = http.DefaultTransport.(*http.Transport)
	}
	t = t.Clone()
	if t.TLSClientConfig == nil {
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	t.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // reachability probe only: trust is a separate resource
	return t
}

func headOne(ctx context.Context, c *http.Client, url string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return Result{URL: url, Err: err}
	}
	resp, err := c.Do(req)
	if err != nil {
		return Result{URL: url, Err: err}
	}
	_ = resp.Body.Close()
	return Result{URL: url, Status: resp.StatusCode, Reachable: resp.StatusCode < 500}
}

// Observe probes the targets and returns the observation of resource on the
// Binary scale. Under env.Raw there is no counter: one all-fail pass is
// UNAVAILABLE. Otherwise the all-fail counter is kept in the state file and the
// answer is AVAILABLE until it reaches threshold.
func Observe(ctx context.Context, p *Prober, env sensorcli.Env, resource string, targets []string, threshold int) levels.Observation {
	results := p.Probe(ctx, targets)
	ok := 0
	for _, r := range results {
		if r.Reachable {
			ok++
			continue
		}
		reason := fmt.Sprintf("status %d", r.Status)
		if r.Err != nil {
			reason = r.Err.Error()
		}
		fmt.Fprintf(env.Stderr, "%s: %s unreachable: %s\n", resource, r.URL, reason)
	}
	level := levels.Binary.Best()
	if ok == 0 {
		fails := 1
		if !env.Raw {
			ex := env.Store.Extra(resource)
			prev, _ := strconv.Atoi(ex[FailKey])
			if prev < 0 {
				prev = 0
			}
			fails = prev + 1
			ex[FailKey] = strconv.Itoa(fails)
			env.Store.SetExtra(resource, ex)
		}
		if env.Raw || fails >= threshold {
			level = levels.Binary.Worst()
		}
	} else if !env.Raw {
		ex := env.Store.Extra(resource)
		if _, had := ex[FailKey]; had {
			delete(ex, FailKey)
			env.Store.SetExtra(resource, ex)
		}
	}
	return levels.Observation{
		Resource: resource,
		Scale:    levels.Binary,
		Level:    level,
		Reading:  &levels.Reading{Value: float64(ok), Unit: "targets_reachable"},
	}
}

// HostURLs turns bare hostnames into the https://<host>/ probe URLs.
func HostURLs(hosts []string) []string {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		out[i] = "https://" + h + "/"
	}
	return out
}

// ConfigureCommon reads the keys shared by both sensors: timeout (duration,
// default 5s) and failures (integer >= 1, default 3). Keys are looked up
// under the given names.
func ConfigureCommon(cfg sensorcli.Config, timeoutKey, failuresKey string, p *Prober, threshold *int) error {
	d, err := cfg.Duration(timeoutKey, DefaultTimeout)
	if err != nil {
		return err
	}
	if d <= 0 {
		return fmt.Errorf("config key %s: must be positive", timeoutKey)
	}
	p.Timeout = d
	if v, ok := cfg.Raw[failuresKey]; ok {
		n, isInt := v.(int64)
		if !isInt || n < 1 {
			return fmt.Errorf("config key %s: want an integer >= 1, got %v", failuresKey, v)
		}
		*threshold = int(n)
	}
	return nil
}

// ValidHost reports whether h is a bare hostname (no scheme, path or space).
func ValidHost(h string) bool {
	return h != "" && !strings.ContainsAny(h, "/ \t?#@")
}
