package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func noEnv(string) string { return "" }

// TestServiceUnitText pins the rendered unit: absolute ExecStart + " serve",
// restart on failure after 5 s, started with the user's default target,
// stdout and stderr appended to serve.log.
func TestServiceUnitText(t *testing.T) {
	got, err := serviceUnit("/home/user/tatitok/dist/tatitok", "/home/user/.local/share/tatitok/serve.log", noEnv)
	if err != nil {
		t.Fatal(err)
	}
	want := serviceMarker + `
[Unit]
Description=tatitok hub (serve) on loopback

[Service]
ExecStart=/home/user/tatitok/dist/tatitok serve
Restart=on-failure
RestartSec=5
StandardOutput=append:/home/user/.local/share/tatitok/serve.log
StandardError=append:/home/user/.local/share/tatitok/serve.log

[Install]
WantedBy=default.target
`
	if got != want {
		t.Fatalf("unit text:\n%s\nwant:\n%s", got, want)
	}

	// A path with a space is quoted, % is escaped, and a variable set at
	// install time is pinned.
	env := map[string]string{"XDG_CONFIG_HOME": "/home/user/cfg"}
	got, err = serviceUnit("/home/user/my apps/tatitok", "/home/user/100%/serve.log", func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		`Environment=XDG_CONFIG_HOME=/home/user/cfg`,
		`ExecStart="/home/user/my apps/tatitok" serve`,
		`StandardOutput=append:/home/user/100%%/serve.log`,
	} {
		if !strings.Contains(got, line+"\n") {
			t.Errorf("unit text has no line %q:\n%s", line, got)
		}
	}
}

// TestServiceInstallRefusesRelativePath: a relative binary path is refused
// before anything is written or systemctl runs.
func TestServiceInstallRefusesRelativePath(t *testing.T) {
	dir := t.TempDir()
	called := false
	systemctl := func(...string) error { called = true; return nil }
	var out bytes.Buffer
	err := installService(dir, "dist/tatitok", filepath.Join(dir, "serve.log"), noEnv, systemctl, &out)
	if err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("install with a relative path: err = %v, want a refusal", err)
	}
	if called {
		t.Error("systemctl ran after the refusal")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files written after the refusal: %v", entries)
	}
}

// TestServiceInstallUninstall: install writes the unit and runs
// daemon-reload, enable, restart; uninstall disables it with --now, removes
// it and reloads. A unit without the marker is left alone by both.
func TestServiceInstallUninstall(t *testing.T) {
	dir := t.TempDir()
	unitDir := filepath.Join(dir, "systemd", "user")
	logPath := filepath.Join(dir, "data", "tatitok", "serve.log")
	var calls []string
	systemctl := func(args ...string) error { calls = append(calls, strings.Join(args, " ")); return nil }
	var out bytes.Buffer
	if err := installService(unitDir, "/home/user/tatitok/dist/tatitok", logPath, noEnv, systemctl, &out); err != nil {
		t.Fatal(err)
	}
	unit := filepath.Join(unitDir, serviceName+".service")
	if !serviceIsOurs(unit) {
		t.Fatalf("%s missing or without the marker", unit)
	}
	if _, err := os.Stat(filepath.Dir(logPath)); err != nil {
		t.Errorf("log directory not created: %v", err)
	}
	if !strings.Contains(out.String(), unit) {
		t.Errorf("install output does not name the unit path: %q", out.String())
	}
	wantCalls := "daemon-reload|enable tatitok-serve.service|restart tatitok-serve.service"
	if got := strings.Join(calls, "|"); got != wantCalls {
		t.Errorf("install systemctl calls = %q, want %q", got, wantCalls)
	}

	calls = nil
	if err := uninstallService(unitDir, systemctl, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) {
		t.Errorf("unit still present after uninstall: %v", err)
	}
	if got, want := strings.Join(calls, "|"), "disable --now tatitok-serve.service|daemon-reload"; got != want {
		t.Errorf("uninstall systemctl calls = %q, want %q", got, want)
	}

	if err := os.WriteFile(unit, []byte("[Service]\nExecStart=/bin/true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	calls = nil
	if err := installService(unitDir, "/home/user/tatitok/dist/tatitok", logPath, noEnv, systemctl, &out); err == nil {
		t.Error("install overwrote a unit without the marker")
	}
	if err := uninstallService(unitDir, systemctl, &out); err == nil {
		t.Error("uninstall removed a unit without the marker")
	}
	if len(calls) != 0 {
		t.Errorf("systemctl ran on a foreign unit: %v", calls)
	}
}

// TestServiceUnitDir: systemd --user reads user units from
// $XDG_CONFIG_HOME/systemd/user when that is set and absolute, else from
// ~/.config/systemd/user; a relative XDG_CONFIG_HOME is ignored.
func TestServiceUnitDir(t *testing.T) {
	t.Setenv("HOME", "/home/user")
	for _, tc := range []struct{ xdg, want string }{
		{"/home/user/cfg", "/home/user/cfg/systemd/user"},
		{"", "/home/user/.config/systemd/user"},
		{"cfg", "/home/user/.config/systemd/user"},
	} {
		got, err := serviceUnitDir(func(k string) string {
			if k == "XDG_CONFIG_HOME" {
				return tc.xdg
			}
			return ""
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("XDG_CONFIG_HOME=%q: unit dir %q, want %q", tc.xdg, got, tc.want)
		}
	}
}
