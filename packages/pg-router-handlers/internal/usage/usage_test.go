package usage

import (
	"context"
	"errors"
	"testing"
)

type fakeSrc struct {
	c   Snapshot
	err error
	id  string
}

func (f *fakeSrc) Consumption(_ context.Context, id string) (Snapshot, error) {
	f.id = id
	return f.c, f.err
}

func TestCcpoolReader_mapsConsumptionIncludingCost(t *testing.T) {
	src := &fakeSrc{c: Snapshot{Model: "m", InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, CostCents: 77}}
	got, err := NewCcpoolReader(src).Read(context.Background(), "ext-1")
	if err != nil {
		t.Fatal(err)
	}
	want := Snapshot{Model: "m", InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, CostCents: 77}
	if got != want || got.Total() != 10 || src.id != "ext-1" {
		t.Errorf("got %+v (id %q), want %+v for ext-1", got, src.id, want)
	}
}

func TestCcpoolReader_errorIsZeroSnapshot(t *testing.T) {
	got, err := NewCcpoolReader(&fakeSrc{err: errors.New("boom")}).Read(context.Background(), "x")
	if err == nil || got != (Snapshot{}) {
		t.Errorf("got %+v, %v; want zero snapshot and the error", got, err)
	}
}

func TestReaderFor_runnerWithoutConsumptionReadsZero(t *testing.T) {
	got, err := ReaderFor(struct{}{}).Read(context.Background(), "x")
	if err != nil || got != (Snapshot{}) {
		t.Errorf("got %+v, %v; want zero", got, err)
	}
	if _, ok := ReaderFor(&fakeSrc{}).(ccpoolReader); !ok {
		t.Error("a consumption-capable runner must get the ccpool reader")
	}
}
