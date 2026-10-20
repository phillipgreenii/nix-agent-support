package collector

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/phillipgreenii/pg-desk-shadow/internal/runner"
	"github.com/phillipgreenii/pg-desk-shadow/internal/schema"
)

// The daemon's two client instances; each watched query is polled on both.
const (
	instancePR = "pr"
	instanceCI = "ci"

	// statusSucceeded is the feed's own word for a source it consulted.
	statusSucceeded = "succeeded"

	// ciConsumerSuffix names the ci instance's consumer: the daemon refuses to
	// create the key (C, ci, Q) while (C, pr, Q) exists (one consumer acting on
	// both feeds would act twice on one CI event), so the two instances are
	// polled as two consumers of the same query.
	ciConsumerSuffix = "-ci"
)

// consumerFor is the change-feed consumer the collector uses on an instance:
// the manifest's consumer on pr, that consumer plus ciConsumerSuffix on ci.
func (c *Collector) consumerFor(instance string) string {
	if instance == instanceCI {
		return c.cfg.Consumer + ciConsumerSuffix
	}
	return c.cfg.Consumer
}

// feedPoll is the outcome of polling one watched query on one instance.
type feedPoll struct {
	instance, query string
	// key is "pr:<query>" or "ci:<query>": the dedupe and state key.
	key string
	// src is the tick row's Sources entry (query prefixed ci: on the ci instance).
	src    schema.Source
	failed bool
	denied bool
	// items are the rows to write: duplicates already dropped.
	items []schema.Item
	// maxSeq is the highest seq among the written, non-replayed items.
	maxSeq int64
	// replayed is set when a cursor_expired answer was followed by a reset
	// re-poll; every item of the poll is then marked Replay.
	replayed bool
	// canAck and ackSeq: a succeeded source answered, and the largest next_seq
	// among the succeeded sources, to be acknowledged after the row is written.
	canAck bool
	ackSeq int64
	// nextSeqs are the next_seq values of the answer's sources, in order.
	nextSeqs []int64
}

// pollFeed polls every watched query on both instances, in a fixed order. A
// non-nil error is a safety refusal of the runner and kills the run.
func (c *Collector) pollFeed(ctx context.Context, started time.Time) ([]*feedPoll, error) {
	var polls []*feedPoll
	for _, q := range c.cfg.Queries {
		for _, inst := range []string{instancePR, instanceCI} {
			p, err := c.pollKey(ctx, inst, q, started)
			if err != nil {
				return nil, err
			}
			polls = append(polls, p)
		}
	}
	return polls, nil
}

// call runs one client request: `<client> --config <cfg> <instance>` with the
// {"op", "args"} request on stdin.
func (c *Collector) call(ctx context.Context, instance, op string, args map[string]any) (runner.Result, error) {
	req, err := json.Marshal(map[string]any{"op": op, "args": args})
	if err != nil {
		return runner.Result{}, err
	}
	argv := []string{c.cfg.ClientBin, "--config", c.cfg.ClientConfig, instance}
	return c.cfg.Exec.RunStdin(ctx, c.cfg.TickTimeout, string(req), c.cfg.ClientEnv, argv...)
}

// ask runs one changes request and decodes the answer.
func (c *Collector) ask(ctx context.Context, instance, query string, reset bool, p *feedPoll) (feedResult, *answerError, error) {
	args := map[string]any{"consumer": c.consumerFor(instance), "query": query}
	if reset {
		args["reset"] = true
	}
	res, err := c.call(ctx, instance, "changes", args)
	if errors.Is(err, runner.ErrSafety) {
		return feedResult{}, nil, err
	}
	if res.Denied {
		p.denied = true
	}
	if err != nil {
		return feedResult{}, &answerError{Code: clientCodeLocal, Message: err.Error()}, nil
	}
	out, aerr := parseChanges(res.Stdout, res.Stderr, res.Exit)
	return out, aerr, nil
}

func (c *Collector) pollKey(ctx context.Context, instance, query string, started time.Time) (*feedPoll, error) {
	m := c.cfg.Manifest
	p := &feedPoll{instance: instance, query: query, key: instance + ":" + query}
	p.src.Query = query
	if instance == instanceCI {
		p.src.Query = "ci:" + query
	}
	ans, aerr, err := c.ask(ctx, instance, query, false, p)
	if err != nil {
		return nil, err
	}
	if aerr != nil && aerr.cursorExpired() {
		p.replayed = true
		ans, aerr, err = c.ask(ctx, instance, query, true, p)
		if err != nil {
			return nil, err
		}
	}
	if aerr != nil {
		p.failed = true
		p.src.Status = "failed"
		p.src.Reason = Scrub(aerr.text(), m.Scrub)
		return p, nil
	}

	// One Sources entry per query and instance: succeeded when every source of
	// the answer is, else the first source that is not (with its reason).
	p.src.Status = statusSucceeded
	if len(ans.Sources) == 0 {
		p.src.Status, p.src.Reason = "unknown", "the answer carried no sources"
	}
	for _, s := range ans.Sources {
		p.nextSeqs = append(p.nextSeqs, s.NextSeq)
		if s.Status == statusSucceeded {
			if !p.canAck || s.NextSeq > p.ackSeq {
				p.ackSeq = s.NextSeq
			}
			p.canAck = true
		} else if p.src.Status == statusSucceeded {
			p.src.Status, p.src.Reason = s.Status, Scrub(s.Reason, m.Scrub)
		}
	}

	origin := schema.OriginDaemonFeed
	if instance == instanceCI {
		origin = schema.OriginDaemonCIFeed
	}
	highest := c.st.HighestSeq[p.key]
	for _, ch := range ans.Changes {
		if !p.replayed && ch.Seq <= highest {
			continue // redelivered after a crash: already written
		}
		it := schema.Item{Kinds: ch.Kinds, Seq: ch.Seq, Origin: origin, At: schema.Format(started), Replay: p.replayed}
		if it.Kinds == nil {
			it.Kinds = []string{}
		}
		for _, f := range ch.Fields {
			it.Fields = append(it.Fields, f.Name)
		}
		if instance == instanceCI {
			it.EntityID, it.CIRollup = ch.Entity.PRID, ch.Entity.Rollup
		} else {
			it.EntityID, it.UpdatedAt = ch.Entity.ID, ch.Entity.UpdatedAt
		}
		if !p.replayed && ch.Seq > p.maxSeq {
			p.maxSeq = ch.Seq
		}
		p.items = append(p.items, it)
	}
	return p, nil
}

// ackFeed acknowledges every key that has a succeeded source with the next_seq
// of its poll, once the tick row holding the poll's rows is on disk. A key with
// no succeeded source is left alone. A failed acknowledgement is logged, not
// fatal: the rows are redelivered and the seq map drops them. It returns how
// many calls the sandbox denied.
func (c *Collector) ackFeed(ctx context.Context, polls []*feedPoll) (denied int, err error) {
	for _, p := range polls {
		if p.failed || !p.canAck {
			continue
		}
		res, rerr := c.call(ctx, p.instance, "changes_ack", map[string]any{"consumer": c.consumerFor(p.instance), "query": p.query, "next_seq": p.ackSeq})
		if errors.Is(rerr, runner.ErrSafety) {
			return denied, rerr
		}
		if res.Denied {
			denied++
		}
		switch {
		case rerr != nil:
			c.logf("ack %s: %s", p.key, Scrub(rerr.Error(), c.cfg.Manifest.Scrub))
		default:
			if _, aerr := parseChanges(res.Stdout, res.Stderr, res.Exit); aerr != nil {
				c.logf("ack %s: %s", p.key, Scrub(aerr.text(), c.cfg.Manifest.Scrub))
			}
		}
	}
	return denied, nil
}
