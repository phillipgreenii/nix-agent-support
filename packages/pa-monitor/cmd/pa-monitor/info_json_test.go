package main

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	pb "github.com/phillipgreenii/pa-monitor/internal/proto"
)

func TestWriteSessionInfoJSON(t *testing.T) {
	v := &pb.SessionView{SessionId: "s1", Status: "working", Model: "claude-sonnet-5"}
	var buf bytes.Buffer
	if err := writeSessionInfoJSON(&buf, v, time.Now().UTC()); err != nil {
		t.Fatalf("writeSessionInfoJSON: %v", err)
	}
	var got sessionJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SessionID != "s1" || got.Status != "working" || got.Model != "claude-sonnet-5" {
		t.Errorf("got %+v", got)
	}
}
