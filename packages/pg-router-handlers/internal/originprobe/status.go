package originprobe

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// WriteStatus lists every watched origin, one line each, in configured order:
// key, class, gated, ignored, since, consecutive failures, last error tail.
// An origin never probed shows class "unchecked". It reads state files only
// and never probes.
func (p *Prober) WriteStatus(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tCLASS\tGATED\tIGNORED\tSINCE\tFAILURES\tLAST_ERROR")
	k := p.cfg.FailureThreshold
	if k < 1 {
		k = 1
	}
	for _, o := range p.cfg.Origins {
		class, since, failures, lastErr := Unchecked, "-", 0, "-"
		if st, ok := p.LoadState(o.Key); ok {
			class = string(st.Class)
			since = st.Since.UTC().Format(time.RFC3339)
			failures = st.ConsecutiveFailures
			if st.LastError != "" {
				lastErr = st.LastError
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%t\t%t\t%s\t%d\t%s\n", o.Key, class, failures >= k, p.Disabled(o.Key), since, failures, lastErr)
	}
	return tw.Flush()
}
