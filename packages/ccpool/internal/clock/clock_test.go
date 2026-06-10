package clock

import (
	"testing"
	"time"
)

func TestFake_Now_isFixedAndAdvanceable(t *testing.T) {
	base := time.Unix(1_000, 0).UTC()
	c := &Fake{T: base}
	if !c.Now().Equal(base) {
		t.Fatalf("Now() = %v, want %v", c.Now(), base)
	}
	c.Advance(90 * time.Second)
	if got := c.Now().Unix(); got != 1_090 {
		t.Fatalf("after Advance, Now().Unix() = %d, want 1090", got)
	}
}
