package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWithParentDeath_cancelsOnEOF(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pr.Close() }()
	ctx, stop := withParentDeath(context.Background(), pr)
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("cancelled while the parent's write end was still open")
	case <-time.After(50 * time.Millisecond):
	}
	_ = pw.Close() // the parent died
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("context not cancelled after the pipe reached EOF")
	}
}

func TestWithParentDeath_nilReaderNeverCancels(t *testing.T) {
	ctx, stop := withParentDeath(context.Background(), nil)
	defer stop()
	select {
	case <-ctx.Done():
		t.Fatal("cancelled with no parent-death pipe")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestParentDeathReader_unsetOrMalformedIsNil(t *testing.T) {
	for _, v := range []string{"", "abc", "2", "-1"} {
		if r := parentDeathReader(func(string) string { return v }); r != nil {
			t.Errorf("value %q: want nil reader", v)
		}
	}
}
