package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// serve --install / --uninstall: a systemd user service that runs serve and
// restarts it after a crash. The unit file is written by --install and never
// committed, like the litellm-refresh and quota-alert timers.

const (
	serviceName   = "tatitok-serve"
	serviceMarker = "# managed-by: tatitok serve --install"
)

// serviceEnv lists the variables tatitok reads to find its database,
// config and harness logs. The systemd user manager may not carry them, so
// one set at install time is pinned in the unit.
var serviceEnv = []string{
	"XDG_DATA_HOME", "XDG_CONFIG_HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME",
	"PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR",
}

// unitQuote quotes a value for a systemd unit line: % → %%, and double-quote
// it (with C escapes) when it holds whitespace, quotes or backslashes.
func unitQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if strings.ContainsAny(s, " \t\n\r\v\f\"'\\") {
		s = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	return s
}

// serviceUnit renders the unit. exe must be absolute (systemd runs it from
// no particular directory); logPath receives stdout and stderr, as the
// hand-started serve's redirect did.
func serviceUnit(exe, logPath string, getenv func(string) string) (string, error) {
	if !filepath.IsAbs(exe) {
		return "", fmt.Errorf("refusing: binary path %q is not absolute", exe)
	}
	if !filepath.IsAbs(logPath) {
		return "", fmt.Errorf("refusing: log path %q is not absolute", logPath)
	}
	lines := []string{
		serviceMarker,
		"[Unit]",
		"Description=tatitok hub (serve) on loopback",
		"",
		"[Service]",
	}
	for _, k := range serviceEnv {
		if v := getenv(k); v != "" {
			lines = append(lines, "Environment="+unitQuote(k+"="+v))
		}
	}
	logAppend := "append:" + strings.ReplaceAll(logPath, "%", "%%")
	lines = append(lines,
		"ExecStart="+unitQuote(exe)+" serve",
		"Restart=on-failure",
		"RestartSec=5",
		"StandardOutput="+logAppend,
		"StandardError="+logAppend,
		"",
		"[Install]",
		"WantedBy=default.target",
	)
	return strings.Join(lines, "\n") + "\n", nil
}

// serviceIsOurs reports whether the unit file carries the marker line.
func serviceIsOurs(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() == serviceMarker {
			return true
		}
	}
	return false
}

func serviceUnitDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// serveLogPath is serve.log beside the default database.
func serveLogPath() string {
	return filepath.Join(filepath.Dir(defaultDBPath()), "serve.log")
}

func systemctlUser(args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl --user %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// installService writes the unit, reloads systemd, enables the service and
// (re)starts it, so a re-install after moving the binary takes effect. A
// unit without the marker is someone else's and is left alone.
func installService(unitDir, exe, logPath string, getenv func(string) string, systemctl func(...string) error, out io.Writer) error {
	text, err := serviceUnit(exe, logPath, getenv)
	if err != nil {
		return err
	}
	path := filepath.Join(unitDir, serviceName+".service")
	if _, err := os.Stat(path); err == nil && !serviceIsOurs(path) {
		return fmt.Errorf("refusing: %s exists and is not ours (no marker line)", path)
	}
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	// systemd creates the log file on start, not its directory.
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", serviceName + ".service"}, {"restart", serviceName + ".service"}} {
		if err := systemctl(args...); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "installed %s\nservice enabled and started; log %s\n", path, logPath)
	return nil
}

// uninstallService stops, disables and removes the unit, only if it
// carries the marker.
func uninstallService(unitDir string, systemctl func(...string) error, out io.Writer) error {
	path := filepath.Join(unitDir, serviceName+".service")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "no %s unit in %s; nothing to do\n", serviceName, unitDir)
		return nil
	}
	if !serviceIsOurs(path) {
		return fmt.Errorf("refusing: %s is not ours (no marker line), leaving it alone", path)
	}
	if err := systemctl("disable", "--now", serviceName+".service"); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", path)
	return systemctl("daemon-reload")
}
