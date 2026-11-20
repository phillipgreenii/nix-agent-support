package daemon

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestAppRunOrdersDrainThenShutdownSteps(t *testing.T) {
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	app := NewApp(AppConfig{
		Interval: time.Hour,
		Startup:  func(context.Context) { got = append(got, "startup") },
		Tick: func(context.Context, string) string {
			got = append(got, "tick")
			cancel()
			return "g"
		},
		Drain:    func(context.Context) { got = append(got, "drain") },
		SetPhase: func(p string) { got = append(got, "phase:"+p) },
		Shutdown: []Step{
			{Run: func() { got = append(got, "a") }},
			{Phase: "b", Run: func() { got = append(got, "b") }},
		},
	})
	app.Run(ctx)
	want := []string{"startup", "tick", "phase:drain", "drain", "a", "phase:b", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
