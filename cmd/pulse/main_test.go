package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

func TestCLI_OnceAndJSON(t *testing.T) {
	fixturePath := "../../testdata/proc_fixtures/tick_01"
	args := []string{
		"--proc-root", fixturePath,
		"--once",
		"--interval", "50ms",
	}

	err := run(args)
	if err != nil {
		t.Fatalf("run --once failed: %v", err)
	}
}

func TestCLI_HeadlessWithTicks(t *testing.T) {
	fixturePath := "../../testdata/proc_fixtures/tick_01"
	tmpDir := t.TempDir()
	alertLog := filepath.Join(tmpDir, "alerts.jsonl")

	args := []string{
		"--proc-root", fixturePath,
		"--headless",
		"--ticks", "3",
		"--interval", "50ms",
		"--alert-log", alertLog,
	}

	err := run(args)
	if err != nil {
		t.Fatalf("run --headless failed: %v", err)
	}
}

func TestCLI_ConfigLoading(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")

	cfgData := model.Config{
		Engine: model.EngineConfig{
			ProcRoot:    "../../testdata/proc_fixtures/tick_01",
			Interval:    50 * time.Millisecond,
			HistorySize: 100,
			UserHZ:      100,
			IrixMode:    true,
		},
		Alert: model.AlertConfig{
			CPUWarnPercent:    80.0,
			SysMemWarnPercent: 85.0,
			MemWarnMB:         1024,
			ZombieWarnCount:   3,
			SustainedTicks:    2,
		},
	}

	raw, err := json.Marshal(cfgData)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	args := []string{
		"--config", cfgPath,
		"--once",
	}

	err = run(args)
	if err != nil {
		t.Fatalf("run with --config failed: %v", err)
	}
}

func TestCLI_HelpAndInvalidFlag(t *testing.T) {
	err := run([]string{"--invalid-flag-1234"})
	if err == nil {
		t.Fatalf("expected error for invalid flag")
	}
	if !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestCLIRejectsInvalidValues(t *testing.T) {
	for _, args := range [][]string{
		{"--headless", "--interval=0"}, {"--headless", "--interval=-1s"},
		{"--once", "--history-size=0"}, {"--once", "--user-hz=0"},
		{"--once", "--cpu-warn=NaN"}, {"--once", "--sys-mem-warn=101"},
		{"--headless", "--ticks=-1"}, {"--json"}, {"unexpected"},
		{"--once", "--headless"},
	} {
		if err := run(args); err == nil {
			t.Errorf("accepted invalid arguments %v", args)
		}
	}
}

func TestHeadlessMissingProcReturnsError(t *testing.T) {
	if err := run([]string{"--headless", "--ticks=1", "--interval=1ms", "--proc-root=" + t.TempDir()}); err == nil {
		t.Fatal("expected collection error")
	}
}
