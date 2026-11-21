package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// EachFile yields the rows AllFiles returns, in the same order; it stops at the
// first callback error and returns it; and it stops on a cancelled context.
func TestEachFile(t *testing.T) {
	s := openCtxStore(t)
	const n = 5
	for i := 0; i < n; i++ {
		seed(t, s)
	}
	want, err := s.AllFiles()
	if err != nil || len(want) != n {
		t.Fatalf("AllFiles: %d rows, %v", len(want), err)
	}
	boom := errors.New("boom")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name     string
		reader   *Reader
		failAt   int // callback returns boom on this 1-based row; 0 never
		wantRows int
		wantErr  error
	}{
		{"all rows in order", &s.Reader, 0, n, nil},
		{"callback error stops the scan", &s.Reader, 3, 3, boom},
		{"cancelled context stops the scan", s.WithContext(cancelled), 0, 0, context.Canceled},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []File
			err := c.reader.EachFile(func(f File) error {
				got = append(got, f)
				if c.failAt > 0 && len(got) == c.failAt {
					return boom
				}
				return nil
			})
			if !errors.Is(err, c.wantErr) {
				t.Fatalf("err = %v, want %v", err, c.wantErr)
			}
			if len(got) != c.wantRows {
				t.Fatalf("%d rows, want %d", len(got), c.wantRows)
			}
			if len(got) > 0 && !reflect.DeepEqual(got, want[:len(got)]) {
				t.Fatalf("rows differ from AllFiles:\n got %+v\nwant %+v", got, want[:len(got)])
			}
		})
	}
}
