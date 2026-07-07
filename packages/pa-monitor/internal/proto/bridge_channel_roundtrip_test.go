package proto

import (
	"testing"

	"google.golang.org/protobuf/proto"
)

// TestDaemonMsgDeliverRoundTrip confirms a DaemonMsg carrying a Deliver
// payload survives a protobuf marshal/unmarshal — this is the wire shape
// the daemon uses to push nudge deliveries to cmux-bridge over
// BridgeChannel.
func TestDaemonMsgDeliverRoundTrip(t *testing.T) {
	in := &DaemonMsg{Kind: &DaemonMsg_Deliver{Deliver: &Deliver{Id: "c1", TargetPid: 4321, Text: "continue"}}}
	b, err := proto.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out DaemonMsg
	if err := proto.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if d := out.GetDeliver(); d == nil || d.GetId() != "c1" || d.GetTargetPid() != 4321 {
		t.Fatalf("got %+v", &out)
	}
}
