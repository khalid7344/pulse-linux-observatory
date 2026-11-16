package alert

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

func TestAlertEvaluator_HysteresisAndResolution(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "alerts.jsonl")

	cfg := model.AlertConfig{
		CPUWarnPercent:    80.0,
		SysMemWarnPercent: 90.0,
		MemWarnMB:         1000,
		ZombieWarnCount:   5,
		SustainedTicks:    3,
		AlertLogPath:      logPath,
	}

	ev, err := NewEvaluator(cfg)
	if err != nil {
		t.Fatalf("NewEvaluator failed: %v", err)
	}
	defer ev.Close()

	ctx := context.Background()
	now := time.Now()

	// Tick 1: System CPU = 85.0% (Breach 1/3)
	v1 := model.SystemView{
		Timestamp:  now,
		CPUPercent: 85.0,
	}
	_ = ev.OnSnapshot(ctx, v1)
	if len(ev.ActiveAlerts()) != 0 {
		t.Fatalf("expected 0 active alerts on tick 1 (hysteresis), got %d", len(ev.ActiveAlerts()))
	}

	// Tick 2: System CPU = 86.0% (Breach 2/3)
	v2 := model.SystemView{
		Timestamp:  now.Add(1 * time.Second),
		CPUPercent: 86.0,
	}
	_ = ev.OnSnapshot(ctx, v2)
	if len(ev.ActiveAlerts()) != 0 {
		t.Fatalf("expected 0 active alerts on tick 2 (hysteresis), got %d", len(ev.ActiveAlerts()))
	}

	// Tick 3: System CPU = 88.0% (Breach 3/3 -> MUST FIRE)
	v3 := model.SystemView{
		Timestamp:  now.Add(2 * time.Second),
		CPUPercent: 88.0,
	}
	_ = ev.OnSnapshot(ctx, v3)
	active := ev.ActiveAlerts()
	if len(active) != 1 {
		t.Fatalf("expected 1 active alert on tick 3, got %d", len(active))
	}
	if active[0].State != "FIRING" || active[0].RuleName != "SystemCPUHigh" {
		t.Errorf("unexpected active alert: %+v", active[0])
	}

	// Tick 4: System CPU = 89.0% (Still breached -> DO NOT DUPLICATE FIRE EVENT)
	v4 := model.SystemView{
		Timestamp:  now.Add(3 * time.Second),
		CPUPercent: 89.0,
	}
	_ = ev.OnSnapshot(ctx, v4)
	if len(ev.ActiveAlerts()) != 1 {
		t.Fatalf("expected still 1 active alert on tick 4, got %d", len(ev.ActiveAlerts()))
	}
	// Verify history only has 1 event so far
	recent := ev.RecentAlerts()
	if len(recent) != 1 {
		t.Fatalf("expected exactly 1 alert event in history, got %d", len(recent))
	}

	// Tick 5: System CPU = 50.0% (Drops below threshold -> MUST RESOLVE)
	v5 := model.SystemView{
		Timestamp:  now.Add(4 * time.Second),
		CPUPercent: 50.0,
	}
	_ = ev.OnSnapshot(ctx, v5)
	if len(ev.ActiveAlerts()) != 0 {
		t.Fatalf("expected 0 active alerts after recovery, got %d", len(ev.ActiveAlerts()))
	}

	recent = ev.RecentAlerts()
	if len(recent) != 2 {
		t.Fatalf("expected 2 alert events in history (FIRING, RESOLVED), got %d", len(recent))
	}
	if recent[1].State != "RESOLVED" {
		t.Errorf("expected second event to be RESOLVED, got %s", recent[1].State)
	}
	if recent[1].ResolvedAt == nil {
		t.Errorf("expected ResolvedAt timestamp to be set")
	}

	// Verify JSONL file content
	ev.Close()
	lines := readLogLines(t, logPath)
	if len(lines) != 2 {
		t.Fatalf("expected 2 JSONL log lines, got %d", len(lines))
	}

	var jsonEv1, jsonEv2 map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &jsonEv1); err != nil {
		t.Fatalf("parse JSON line 1: %v", err)
	}
	if jsonEv1["state"] != "FIRING" || jsonEv1["rule"] != "SystemCPUHigh" {
		t.Errorf("unexpected JSON line 1: %v", jsonEv1)
	}

	if err := json.Unmarshal([]byte(lines[1]), &jsonEv2); err != nil {
		t.Fatalf("parse JSON line 2: %v", err)
	}
	if jsonEv2["state"] != "RESOLVED" || jsonEv2["rule"] != "SystemCPUHigh" {
		t.Errorf("unexpected JSON line 2: %v", jsonEv2)
	}
}

func TestAlertEvaluator_TransientSpikeNoFlap(t *testing.T) {
	cfg := model.AlertConfig{
		CPUWarnPercent: 80.0,
		SustainedTicks: 3,
	}
	ev, _ := NewEvaluator(cfg)
	ctx := context.Background()
	now := time.Now()

	// Tick 1: Spike to 95%
	_ = ev.OnSnapshot(ctx, model.SystemView{Timestamp: now, CPUPercent: 95.0})
	// Tick 2: Spike to 92%
	_ = ev.OnSnapshot(ctx, model.SystemView{Timestamp: now.Add(1 * time.Second), CPUPercent: 92.0})
	// Tick 3: Drops to 40% before sustained ticks reached
	_ = ev.OnSnapshot(ctx, model.SystemView{Timestamp: now.Add(2 * time.Second), CPUPercent: 40.0})

	if len(ev.ActiveAlerts()) != 0 {
		t.Fatalf("transient spike should not have fired an alert")
	}
	if len(ev.RecentAlerts()) != 0 {
		t.Fatalf("transient spike should have 0 alert events in history")
	}
}

func TestAlertEvaluator_ProcessTerminationResolution(t *testing.T) {
	cfg := model.AlertConfig{
		CPUWarnPercent: 50.0,
		SustainedTicks: 2,
	}
	ev, _ := NewEvaluator(cfg)
	ctx := context.Background()
	now := time.Now()

	pKey := model.ProcessKey{PID: 999, StartTime: 1234}
	pHigh := model.ProcessMetrics{
		Key:            pKey,
		Comm:           "leak-proc",
		CPUPercentNorm: 90.0,
	}

	// Ticks 1 & 2: Process breaches threshold
	_ = ev.OnSnapshot(ctx, model.SystemView{Timestamp: now, Processes: []model.ProcessMetrics{pHigh}})
	_ = ev.OnSnapshot(ctx, model.SystemView{Timestamp: now.Add(1 * time.Second), Processes: []model.ProcessMetrics{pHigh}})

	if len(ev.ActiveAlerts()) != 1 {
		t.Fatalf("expected 1 active alert for process, got %d", len(ev.ActiveAlerts()))
	}

	// Tick 3: Process terminates (absent from Processes)
	_ = ev.OnSnapshot(ctx, model.SystemView{Timestamp: now.Add(2 * time.Second), Processes: nil})

	if len(ev.ActiveAlerts()) != 0 {
		t.Fatalf("expected 0 active alerts after process termination, got %d", len(ev.ActiveAlerts()))
	}

	recent := ev.RecentAlerts()
	if len(recent) != 2 {
		t.Fatalf("expected 2 events in history, got %d", len(recent))
	}
	if recent[1].State != "RESOLVED" {
		t.Errorf("expected RESOLVED state upon process termination, got %s", recent[1].State)
	}
}

func TestAlertEvaluator_ConcurrentAccess(t *testing.T) {
	cfg := model.AlertConfig{
		CPUWarnPercent: 50.0,
		SustainedTicks: 2,
	}
	ev, _ := NewEvaluator(cfg)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				view := model.SystemView{
					Timestamp:  time.Now(),
					CPUPercent: float64(40 + (j % 30)),
					Processes: []model.ProcessMetrics{
						{
							Key:            model.ProcessKey{PID: 100 + workerID, StartTime: 1000},
							Comm:           "worker",
							CPUPercentNorm: float64(30 + (j % 40)),
						},
					},
				}
				_ = ev.OnSnapshot(ctx, view)
				_ = ev.ActiveAlerts()
				_ = ev.RecentAlerts()
			}
		}(i)
	}
	wg.Wait()
}

func readLogLines(t *testing.T, path string) []string {
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log file %q: %v", path, err)
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		text := scanner.Text()
		if text != "" {
			lines = append(lines, text)
		}
	}
	return lines
}
