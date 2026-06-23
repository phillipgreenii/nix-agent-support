package otel

import (
	"context"
	"testing"
)

func TestBuildResourceMergesEnvAndKeepsServiceName(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "host.name=mbp-02")
	res, err := buildResource(context.Background(), "pa-monitor", "v1")
	if err != nil {
		t.Fatal(err)
	}
	var sawHost, sawService bool
	for _, a := range res.Attributes() {
		if string(a.Key) == "host.name" && a.Value.AsString() == "mbp-02" {
			sawHost = true
		}
		if string(a.Key) == "service.name" && a.Value.AsString() == "pa-monitor" {
			sawService = true
		}
	}
	if !sawHost {
		t.Error("env resource attr host.name not merged")
	}
	if !sawService {
		t.Error("explicit service.name must survive the merge")
	}
}
