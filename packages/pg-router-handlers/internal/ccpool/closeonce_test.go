package ccpool

import (
	"context"
	"errors"
	"testing"
)

// closeRecorder is a Runner whose List serves rows and whose Close records calls.
type closeRecorder struct {
	Runner  // unused methods panic via the nil embedded interface
	rows    []Session
	listErr error
	closed  []string
	purged  []bool
}

func (c *closeRecorder) List(context.Context) ([]Session, error) { return c.rows, c.listErr }
func (c *closeRecorder) Close(_ context.Context, id string, purge bool) error {
	c.closed = append(c.closed, id)
	c.purged = append(c.purged, purge)
	return nil
}

func TestCloseIfOpen_cases(t *testing.T) {
	cases := []struct {
		name      string
		rows      []Session
		listErr   error
		purge     bool
		wantClose bool
	}{
		{"open live row is closed", []Session{{ExternalID: "s", Live: true}}, nil, false, true},
		{"closed and gone row is not closed again", []Session{{ExternalID: "s", CloseReason: "handler"}}, nil, false, false},
		{"reason but still live (teardown failed) is retried", []Session{{ExternalID: "s", CloseReason: "handler", Live: true}}, nil, false, true},
		{"absent row falls through to a real close", nil, nil, false, true},
		{"list failure falls through to a real close", nil, errors.New("list: transient"), false, true},
		{"another session's closed row is irrelevant", []Session{{ExternalID: "other", CloseReason: "handler"}}, nil, false, true},
		{"purge always closes (it deletes the row)", []Session{{ExternalID: "s", CloseReason: "handler"}}, nil, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &closeRecorder{rows: tc.rows, listErr: tc.listErr}
			closed, err := CloseIfOpen(context.Background(), r, "s", tc.purge)
			if err != nil {
				t.Fatal(err)
			}
			if closed != tc.wantClose || (len(r.closed) == 1) != tc.wantClose {
				t.Errorf("closed=%v calls=%v, want close=%v", closed, r.closed, tc.wantClose)
			}
		})
	}
}
