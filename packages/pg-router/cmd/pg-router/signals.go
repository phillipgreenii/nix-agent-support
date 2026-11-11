package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// armSignals is the one signal registration of a run or run-until-idle
// process: the returned context ends on the first of sigs, and the registration
// is released as soon as that context ends, so a second signal takes its
// default action (kills the process) while the first drives a graceful
// shutdown. The returned stop also releases it (idempotent). notify is
// signal.NotifyContext; tests pass a fake.
func armSignals(notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc), sigs ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, stop := notify(context.Background(), sigs...)
	context.AfterFunc(ctx, stop)
	return ctx, stop
}

// armRunSignals arms SIGINT and SIGTERM through the real signal.NotifyContext.
func armRunSignals() (context.Context, context.CancelFunc) {
	return armSignals(signal.NotifyContext, os.Interrupt, syscall.SIGTERM)
}
