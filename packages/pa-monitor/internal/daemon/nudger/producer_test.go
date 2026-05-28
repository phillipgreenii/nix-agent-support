package nudger

import (
	"testing"
	"time"

	"github.com/phillipgreenii/pa-monitor/internal/core/aggregate"
)

func TestProducerInterfaceCompliance(t *testing.T) {
	// Compile-time assertions that each concrete producer type satisfies Producer.
	var _ Producer = (*WindowResetProducer)(nil)
	var _ Producer = (*DisruptProducer)(nil)
	var _ Producer = (*ManualProducer)(nil)
}

func TestTickContextZeroValueUsable(t *testing.T) {
	ctx := TickContext{Now: time.Now(), Tree: &aggregate.Tree{}}
	if ctx.Now.IsZero() {
		t.Error("Now should be set")
	}
}
