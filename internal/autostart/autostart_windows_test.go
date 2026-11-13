//go:build windows

package autostart

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnableDisableRoundTrip(t *testing.T) {
	// Always start from a clean state.
	_, _ = Disable()
	if st := Info(); st.Enabled {
		t.Fatalf("expected disabled state, got %+v", st)
	}

	exe := filepath.Join(t.TempDir(), "WailsTools.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	st, err := Enable(Options{ExePath: exe})
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("not enabled: %+v", st)
	}
	if !strings.Contains(st.Command, "WailsTools.exe") {
		t.Fatalf("unexpected command: %q", st.Command)
	}
	if !strings.Contains(st.Location, "CurrentVersion\\Run") {
		t.Fatalf("unexpected location: %q", st.Location)
	}

	// Info must reflect the registry, not just the in-memory flag.
	if fresh := Info(); !fresh.Enabled || fresh.Command != st.Command {
		t.Fatalf("registry state mismatch: %+v", fresh)
	}

	st, err = Disable()
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if st.Enabled {
		t.Fatalf("still enabled: %+v", st)
	}
	if fresh := Info(); fresh.Enabled {
		t.Fatalf("registry entry not removed: %+v", fresh)
	}
}

func TestEnableRejectsMissingExecutable(t *testing.T) {
	if _, err := Enable(Options{ExePath: filepath.Join(t.TempDir(), "missing.exe")}); err == nil {
		t.Fatal("expected missing executable error")
	}
}
