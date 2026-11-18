package answer

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phillipgreenii/pg-router/schemas"
)

var t0 = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

func good() Answer {
	return Answer{Resource: "disk", Level: "OK", Reading: &Reading{Value: 31.5, Unit: "GiB"}, ObservedAt: t0, ValidForS: 180, Owner: "host-sensor"}
}

func TestPrinter_emitsOneSchemaValidLine(t *testing.T) {
	var buf bytes.Buffer
	if err := (Printer{W: &buf}).Print(good()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("want exactly one JSONL line, got %q", out)
	}
	if err := schemas.ValidateLevelAnswer([]byte(strings.TrimSpace(out))); err != nil {
		t.Fatalf("printed line fails the level-answer schema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatal(err)
	}
	if m["schemaVersion"] != "1" || m["observed_at"] != "2026-11-11T12:00:00Z" || m["owner"] != "host-sensor" {
		t.Fatalf("fields = %v", m)
	}
}

func TestPrinter_omitsReadingWhenAbsent(t *testing.T) {
	a := good()
	a.Reading = nil
	var buf bytes.Buffer
	if err := (Printer{W: &buf}).Print(a); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "reading") {
		t.Fatalf("reading must be omitted: %s", buf.String())
	}
}

func TestPrinter_rejectsAnswerTheSchemaRejects(t *testing.T) {
	bad := []Answer{
		{Resource: "Disk!", Level: "OK", ObservedAt: t0, ValidForS: 180, Owner: "x"}, // resource pattern
		{Resource: "disk", Level: "ok", ObservedAt: t0, ValidForS: 180, Owner: "x"},  // level pattern
		{Resource: "disk", Level: "OK", ObservedAt: t0, ValidForS: 0, Owner: "x"},    // valid_for_s minimum
		{Resource: "disk", Level: "OK", ObservedAt: t0, ValidForS: 180},              // owner required
	}
	for i, a := range bad {
		var buf bytes.Buffer
		err := (Printer{W: &buf}).Print(a)
		if err == nil || !errors.Is(err, schemas.ErrValidation) {
			t.Errorf("case %d: err = %v, want a validation error", i, err)
		}
		if buf.Len() != 0 {
			t.Errorf("case %d: wrote %q although invalid", i, buf.String())
		}
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("pipe closed") }

func TestPrinter_surfacesWriteError(t *testing.T) {
	if err := (Printer{W: failWriter{}}).Print(good()); err == nil {
		t.Fatal("want the write error")
	}
}

func TestAnswer_roundTrip(t *testing.T) {
	raw, err := json.Marshal(good())
	if err != nil {
		t.Fatal(err)
	}
	var back Answer
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.ObservedAt.Equal(t0) || back.Resource != "disk" || back.Reading == nil || back.Reading.Value != 31.5 {
		t.Fatalf("back = %+v", back)
	}
	for _, in := range []string{`{`, `{"observed_at":"yesterday"}`, `{"observed_at":"2026-11-11T12:00:00Z","valid_for_s":"x"}`} {
		if err := json.Unmarshal([]byte(in), &back); err == nil {
			t.Errorf("Unmarshal(%q) must fail", in)
		}
	}
}

func TestCache_freshnessIsJudgedByObservedAt(t *testing.T) {
	c := NewCache("")
	a := good()
	c.Put(a)
	if _, ok := c.Get("disk", t0.Add(179*time.Second), 180*time.Second); !ok {
		t.Fatal("an answer inside observed_at+maxAge must be served")
	}
	if _, ok := c.Get("disk", t0.Add(180*time.Second), 180*time.Second); ok {
		t.Fatal("an answer at exactly observed_at+maxAge is stale")
	}
	if _, ok := c.Get("cpu", t0, time.Hour); ok {
		t.Fatal("an absent resource is a miss")
	}
	older := a
	older.Level = "TIGHT"
	older.ObservedAt = t0.Add(-time.Minute)
	c.Put(older)
	if got, _ := c.Get("disk", t0, time.Hour); got.Level != "OK" {
		t.Fatalf("an older observation must not replace a newer one, got %s", got.Level)
	}
	newer := a
	newer.Level = "PLENTY"
	newer.ObservedAt = t0.Add(time.Minute)
	c.Put(newer)
	if got, _ := c.Get("disk", t0.Add(time.Minute), time.Hour); got.Level != "PLENTY" {
		t.Fatalf("a newer observation replaces: %s", got.Level)
	}
	if err := c.Save(); err != nil {
		t.Fatalf("memory cache Save must be a no-op: %v", err)
	}
}

func TestCache_persistsAcrossRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "host-sensor.cache.json")
	c := NewCache(path)
	c.Put(good())
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	c2 := OpenCache(path)
	got, ok := c2.Get("disk", t0.Add(time.Second), time.Minute)
	if !ok || got.Level != "OK" || !got.ObservedAt.Equal(t0) {
		t.Fatalf("got %+v ok=%v", got, ok)
	}
}

func TestOpenCache_missingOrMalformedIsEmpty(t *testing.T) {
	dir := t.TempDir()
	if c := OpenCache(filepath.Join(dir, "none")); len(c.answers) != 0 {
		t.Fatal("missing file must be an empty cache")
	}
	bad := filepath.Join(dir, "bad")
	_ = os.WriteFile(bad, []byte("[1,2"), 0o600)
	if c := OpenCache(bad); len(c.answers) != 0 {
		t.Fatal("malformed file must be an empty cache")
	}
	null := filepath.Join(dir, "null")
	_ = os.WriteFile(null, []byte("null"), 0o600)
	c := OpenCache(null)
	c.Put(good()) // must not panic on a nil map
	if _, ok := c.Get("disk", t0, time.Hour); !ok {
		t.Fatal("cache from null must still work")
	}
}

func TestCache_saveErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, []byte("x"), 0o600)
	c := NewCache(filepath.Join(blocker, "sub", "c.json"))
	if err := c.Save(); err == nil {
		t.Fatal("Save under a file must fail")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "c.json")
	_ = os.MkdirAll(filepath.Join(target, "child"), 0o755)
	if err := NewCache(target).Save(); err == nil {
		t.Fatal("rename onto a directory must fail")
	}
}

func TestEncode_unmarshalableReadingIsAnError(t *testing.T) {
	a := good()
	a.Reading = &Reading{Value: math.NaN(), Unit: "GiB"}
	if _, err := Encode(a); err == nil {
		t.Fatal("a NaN reading cannot be encoded as JSON")
	}
}

func TestCache_saveIntoReadOnlyDirFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := NewCache(filepath.Join(dir, "c.json")).Save(); err == nil {
		t.Skip("directory permissions not enforced (running as root?)")
	}
}
