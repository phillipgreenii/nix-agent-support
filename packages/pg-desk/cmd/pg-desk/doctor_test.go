package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func runDoctorCmd(t *testing.T) (stdout string, err error) {
	t.Helper()
	c, _, ferr := rootCmd.Find([]string{"doctor"})
	if ferr != nil {
		t.Fatalf("rootCmd has no doctor subcommand: %v", ferr)
	}
	var buf bytes.Buffer
	c.SetContext(context.Background())
	c.SetOut(&buf)
	err = c.RunE(c, nil)
	return buf.String(), err
}

func stubDoctorSeams(t *testing.T, lookPathErr, configValidateErr, serveErr error) {
	t.Helper()
	origLookPath := doctorLookPath
	origConfigValidate := doctorConfigValidate
	origServeReachable := doctorServeReachable
	t.Cleanup(func() {
		doctorLookPath = origLookPath
		doctorConfigValidate = origConfigValidate
		doctorServeReachable = origServeReachable
	})
	doctorLookPath = func(name string) (string, error) {
		if lookPathErr != nil {
			return "", lookPathErr
		}
		return "/usr/local/bin/" + name, nil
	}
	doctorConfigValidate = func(ctx context.Context) error { return configValidateErr }
	doctorServeReachable = func(addr string) error { return serveErr }
}

func TestDoctorAllChecksPass(t *testing.T) {
	withOpenSeams(t, openTestConfig("o/r"), nil)
	stubDoctorSeams(t, nil, nil, nil)

	stdout, err := runDoctorCmd(t)
	if err != nil {
		t.Fatalf("doctor: %v, want every check to pass", err)
	}
	if !strings.Contains(stdout, "config: ok") {
		t.Errorf("stdout missing config check: %s", stdout)
	}
	if !strings.Contains(stdout, "stranded cycles: 0") {
		t.Errorf("stdout missing the stranded-cycle report: %s", stdout)
	}
}

func TestDoctorFailsWhenPgConnectorMissing(t *testing.T) {
	withOpenSeams(t, openTestConfig("o/r"), nil)
	stubDoctorSeams(t, errors.New("not found"), nil, nil)

	stdout, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure naming the missing binary")
	}
	if !strings.Contains(stdout, "FAIL") {
		t.Errorf("stdout does not mark the failing check: %s", stdout)
	}
	if !strings.Contains(err.Error(), "pg-connector on PATH") {
		t.Errorf("error %q does not name the failing check", err)
	}
}

func TestDoctorFailsWhenConfigValidateFails(t *testing.T) {
	withOpenSeams(t, openTestConfig("o/r"), nil)
	stubDoctorSeams(t, nil, errors.New("exit status 1"), nil)

	_, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "config validate") {
		t.Errorf("error %q does not name the failing check", err)
	}
}

func TestDoctorFailsWhenServeUnreachable(t *testing.T) {
	withOpenSeams(t, openTestConfig("o/r"), nil)
	stubDoctorSeams(t, nil, nil, errors.New("connection refused"))

	_, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "serve reachable") {
		t.Errorf("error %q does not name the failing check", err)
	}
}

func TestDoctorReportsMultipleFailures(t *testing.T) {
	withOpenSeams(t, openTestConfig("o/r"), nil)
	stubDoctorSeams(t, errors.New("not found"), errors.New("exit status 1"), errors.New("refused"))

	_, err := runDoctorCmd(t)
	if err == nil {
		t.Fatal("doctor: error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "3 check(s) failed") {
		t.Errorf("error %q does not report all 3 failures", err)
	}
}
