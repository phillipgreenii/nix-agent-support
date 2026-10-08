package zone_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/phillipgreenii/phillipgreenii-nix-agent-support/packages/pg-task-focus/internal/zone"
)

func TestHostZone(t *testing.T) {
	const localtime = "/etc/localtime"
	tests := []struct {
		name     string
		tz       string
		target   string
		linkErr  error
		wantZone string
		wantErr  []string // substrings the failure message must name
	}{
		{name: "TZ wins over the link", tz: "America/New_York", target: "/usr/share/zoneinfo/Europe/Paris", wantZone: "America/New_York"},
		{name: "TZ wins when the link is unreadable", tz: "America/New_York", linkErr: errors.New("no such file"), wantZone: "America/New_York"},
		{name: "a leading colon in TZ is stripped", tz: ":America/New_York", wantZone: "America/New_York"},
		{name: "only one leading colon is stripped", tz: "::America/New_York", target: "/usr/share/zoneinfo/Asia/Tokyo", wantZone: "Asia/Tokyo"},
		{name: "unset TZ follows the Linux link", target: "/usr/share/zoneinfo/Europe/Paris", wantZone: "Europe/Paris"},
		{name: "unset TZ follows the macOS link", target: "/var/db/timezone/zoneinfo/Asia/Tokyo", wantZone: "Asia/Tokyo"},
		{name: "a relative link target is read after its last zoneinfo segment", target: "../usr/share/zoneinfo/Europe/Paris", wantZone: "Europe/Paris"},
		{name: "the last zoneinfo segment decides", target: "/zoneinfo/store/zoneinfo/Asia/Kolkata", wantZone: "Asia/Kolkata"},
		{name: "an invalid TZ falls back to the link", tz: "PST", target: "/usr/share/zoneinfo/Europe/Paris", wantZone: "Europe/Paris"},
		{name: "TZ=Local falls back to a link that names a zone", tz: "Local", target: "/usr/share/zoneinfo/Europe/Paris", wantZone: "Europe/Paris"},
		{name: "TZ=Local with a link that names no zone fails", tz: "Local", target: "/etc/some-file", wantErr: []string{"Local", "/etc/some-file"}},
		{name: "TZ=Local with an unreadable link fails", tz: "Local", linkErr: errors.New("permission denied"), wantErr: []string{"Local", "permission denied"}},
		{name: "unset TZ with a link naming no zone fails", target: "/etc/some-file", wantErr: []string{"TZ", "/etc/some-file"}},
		{name: "a link naming an unknown zone fails", target: "/usr/share/zoneinfo/Nowhere/Land", wantErr: []string{"TZ", "Nowhere/Land"}},
		{name: "a link into a lookalike directory is not a zone", target: "/usr/share/zoneinfo.default/Europe/Paris", wantErr: []string{"zoneinfo.default"}},
		{name: "an empty zoneinfo tail fails", target: "/usr/share/zoneinfo/", wantErr: []string{"/usr/share/zoneinfo/"}},
		{name: "unset TZ and an unreadable link fails", linkErr: errors.New("no such file"), wantErr: []string{"TZ", "no such file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(k string) string {
				if k != "TZ" {
					t.Errorf("getenv called with %q, want only TZ", k)
				}
				return tt.tz
			}
			readlink := func(p string) (string, error) {
				if p != localtime {
					t.Errorf("readlink called with %q, want %q", p, localtime)
				}
				return tt.target, tt.linkErr
			}
			z, err := zone.Host(getenv, readlink)
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("Host() = %q, want an error", z.Name())
				}
				for _, sub := range tt.wantErr {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q does not name %q", err, sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Host() error: %v", err)
			}
			if z.Name() != tt.wantZone {
				t.Fatalf("Host() = %q, want %q", z.Name(), tt.wantZone)
			}
		})
	}
}

func TestHostZoneErrorNamesBothInputs(t *testing.T) {
	_, err := zone.Host(
		func(string) string { return "Local" },
		func(string) (string, error) { return "/etc/odd-target", nil },
	)
	if err == nil {
		t.Fatal("Host() succeeded, want an error")
	}
	for _, sub := range []string{`TZ="Local"`, `"/etc/odd-target"`, "/etc/localtime"} {
		if !strings.Contains(err.Error(), sub) {
			t.Errorf("error %q does not contain %q", err, sub)
		}
	}
}
