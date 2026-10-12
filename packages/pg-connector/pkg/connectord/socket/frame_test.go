package socket

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func readFrame(t *testing.T, in string, max int) ([]byte, error) {
	t.Helper()
	return ReadFrame(bufio.NewReaderSize(strings.NewReader(in), 16), max)
}

func TestReadFrameCap(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		max     int
		want    string
		wantErr error
	}{
		{"line", "abc\n", 10, "abc", nil},
		{"exactly the cap", strings.Repeat("a", 10) + "\n", 10, strings.Repeat("a", 10), nil},
		{"one over the cap", strings.Repeat("a", 11) + "\n", 10, "", ErrFrameTooLarge},
		{"no newline at the cap", strings.Repeat("a", 10), 10, strings.Repeat("a", 10), nil},
		{"no newline over the cap", strings.Repeat("a", 11), 10, "", ErrFrameTooLarge},
		{"far over the cap", strings.Repeat("a", 5000), 10, "", ErrFrameTooLarge},
		{"empty input", "", 10, "", io.EOF},
		{"only the first line", "one\ntwo\n", 10, "one", nil},
		{"empty line", "\n", 10, "", nil},
	} {
		got, err := readFrame(t, tc.in, tc.max)
		if !errors.Is(err, tc.wantErr) || string(got) != tc.want {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestVersionWindows(t *testing.T) {
	for _, tc := range []struct {
		current, got   int
		accepts, reply bool
	}{
		{3, 3, true, true},
		{3, 2, true, true},
		{3, 4, false, true},
		{3, 1, false, false},
		{3, 5, false, false},
		{1, 1, true, true},
		{1, 0, false, false}, // 0 is "no field", never a version
		{2, 1, true, true},
		{1, 2, false, true},
	} {
		if got := Accepts(tc.current, tc.got); got != tc.accepts {
			t.Errorf("Accepts(N=%d, %d) = %v, want %v", tc.current, tc.got, got, tc.accepts)
		}
		if got := ReplyCompatible(tc.current, tc.got); got != tc.reply {
			t.Errorf("ReplyCompatible(mine=%d, %d) = %v, want %v", tc.current, tc.got, got, tc.reply)
		}
	}
}

// The two frame shapes, field for field: the names are the wire contract.
func TestFrameShapes(t *testing.T) {
	var b bytes.Buffer
	err := WriteFrame(&b, RequestFrame{
		SocketProtocol: 1, ClientBuild: "b1", Instance: "pr", DeadlineMS: 2500,
		Request: json.RawMessage(`{"op":"list","args":{"q":"<a&b>"}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"socket_protocol":1,"client_build":"b1","instance":"pr","deadline_ms":2500,"request":{"op":"list","args":{"q":"<a&b>"}}}` + "\n"
	if b.String() != want {
		t.Errorf("request frame\n got %s\nwant %s", b.String(), want)
	}

	b.Reset()
	err = WriteFrame(&b, ReplyFrame{SocketProtocol: 1, DaemonBuild: "d1", Response: json.RawMessage(`{"protocolVersion":1,"result":{"n":1}}`)})
	if err != nil {
		t.Fatal(err)
	}
	want = `{"socket_protocol":1,"daemon_build":"d1","response":{"protocolVersion":1,"result":{"n":1}}}` + "\n"
	if b.String() != want {
		t.Errorf("reply frame\n got %s\nwant %s", b.String(), want)
	}
}
