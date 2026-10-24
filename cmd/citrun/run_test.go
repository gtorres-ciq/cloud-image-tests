// cmd/citrun/run_test.go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunDirFresh(t *testing.T) {
	dir := t.TempDir()
	if err := runDirFresh(dir); err != nil {
		t.Fatalf("runDirFresh: want nil for a dir with no state.json, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runDirFresh(dir); err == nil {
		t.Fatal("runDirFresh: want error once state.json exists, got nil")
	}
}

func TestCmdRunTierFlag(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "citrun.yaml")
	if err := os.WriteFile(configPath, []byte(tierConfig), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := cmdRun([]string{"-config", configPath, "-dry-run", "-tier", "nope"}); code != 2 {
		t.Errorf("unknown tier: code = %d, want 2", code)
	}
	// Default and explicit "all" expand the full matrix without error.
	for _, tier := range []string{"all", ""} {
		args := []string{"-config", configPath, "-dry-run"}
		if tier != "" {
			args = append(args, "-tier", tier)
		}
		if code := cmdRun(args); code != 0 {
			t.Errorf("tier %q: code = %d, want 0", tier, code)
		}
	}
	if code := cmdRun([]string{"-config", configPath, "-dry-run", "-tier", "1"}); code != 0 {
		t.Errorf("tier 1: code = %d, want 0", code)
	}
}

func TestFormatTotalTime(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{66*time.Minute + 33*time.Second, "66m 33s"},
		{59 * time.Minute, "59m 00s"},
		{90 * time.Second, "1m 30s"},
		{42 * time.Second, "42s"},
		{0, "0s"},
	}
	for _, c := range cases {
		if got := formatTotalTime(c.in); got != c.want {
			t.Errorf("formatTotalTime(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTierConfigYAMLNamesQuotedSoOneIsNotOctal(t *testing.T) {
	if !strings.Contains(tierConfig, `"1":`) {
		t.Fatalf("tier keys must be quoted in YAML so 1 stays a string: %s", tierConfig)
	}
}
