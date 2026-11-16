package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

func sampleSystemView() model.SystemView {
	return model.SystemView{
		TickNumber:        10,
		Timestamp:         time.Now(),
		SampleInterval:    1 * time.Second,
		NumCores:          4,
		UserHZ:            100,
		CPUPercent:        25.5,
		MemTotalBytes:     16 * 1024 * 1024 * 1024,
		MemUsedBytes:      4 * 1024 * 1024 * 1024,
		MemAvailableBytes: 12 * 1024 * 1024 * 1024,
		MemPercent:        25.0,
		SwapTotalBytes:    4 * 1024 * 1024 * 1024,
		SwapUsedBytes:     512 * 1024 * 1024,
		TotalProcs:        3,
		RunningProcs:      1,
		ZombieCount:       1,
		Processes: []model.ProcessMetrics{
			{
				Key:            model.ProcessKey{PID: 101, StartTime: 5000},
				UID:            1000,
				Comm:           "nginx",
				Cmdline:        "nginx: master process",
				State:          "S",
				CPUPercentNorm: 5.0,
				CPUPercentIrix: 20.0,
				MemPercent:     2.0,
				RSSBytes:       20 * 1024 * 1024,
				VMSBytes:       100 * 1024 * 1024,
				PSSBytes:       12 * 1024 * 1024,
				PSSValid:       true,
				NumThreads:     1,
			},
			{
				Key:            model.ProcessKey{PID: 202, StartTime: 6000},
				UID:            1000,
				Comm:           "firefox",
				Cmdline:        "/usr/lib/firefox/firefox",
				State:          "R",
				CPUPercentNorm: 45.0,
				CPUPercentIrix: 180.0,
				MemPercent:     15.0,
				RSSBytes:       2 * 1024 * 1024 * 1024,
				VMSBytes:       4 * 1024 * 1024 * 1024,
				PSSBytes:       0,
				PSSValid:       false,
				NumThreads:     16,
			},
			{
				Key:            model.ProcessKey{PID: 404, StartTime: 7000},
				UID:            1000,
				Comm:           "defunct",
				Cmdline:        "[defunct]",
				State:          "Z",
				CPUPercentNorm: 0.0,
				CPUPercentIrix: 0.0,
				MemPercent:     0.0,
				RSSBytes:       0,
				VMSBytes:       0,
				NumThreads:     1,
			},
		},
		History: []model.SystemHistoryPoint{
			{Timestamp: time.Now(), CPUPercent: 10.0, MemPercent: 20.0},
			{Timestamp: time.Now(), CPUPercent: 25.5, MemPercent: 25.0},
		},
	}
}

func TestApp_RenderingAndSorting(t *testing.T) {
	app := NewApp(nil, nil, 1*time.Second)
	app.width = 120
	app.height = 35

	// Send snapshot message
	view := sampleSystemView()
	_, _ = app.Update(SnapshotMsg{
		View: view,
		ActiveAlerts: []model.AlertEvent{
			{
				ID:       "sys_cpu_high",
				RuleName: "SystemCPUHigh",
				State:    "FIRING",
				Message:  "System CPU is 95.0%",
			},
		},
	})

	output := app.View()
	if !strings.Contains(output, "PULSE LINUX OBSERVATORY") {
		t.Errorf("expected title in view output")
	}
	if !strings.Contains(output, "nginx") || !strings.Contains(output, "firefox") {
		t.Errorf("expected process names in view output")
	}
	if !strings.Contains(output, "FIRING") {
		t.Errorf("expected active alert in view output")
	}

	// Default sort is CPU descending: firefox (45%) should be first
	if len(app.filteredProcs) != 3 || app.filteredProcs[0].Key.PID != 202 {
		t.Fatalf("expected PID 202 first in CPU desc sort, got %v", app.filteredProcs[0].Key.PID)
	}

	// Press 'p' -> Sort by PID ascending: PID 101 should be first
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if app.filteredProcs[0].Key.PID != 101 {
		t.Fatalf("expected PID 101 first in PID asc sort, got %d", app.filteredProcs[0].Key.PID)
	}

	// Press 'm' -> Sort by Memory descending: PID 202 (2 GB) should be first
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if app.filteredProcs[0].Key.PID != 202 {
		t.Fatalf("expected PID 202 first in Mem desc sort, got %d", app.filteredProcs[0].Key.PID)
	}

	// Press 'i' -> Toggle Irix mode
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if !app.irixMode {
		t.Errorf("expected Irix mode enabled")
	}
	output = app.View()
	if !strings.Contains(output, "IRIX") {
		t.Errorf("expected IRIX indicator in view")
	}
}

func TestApp_FilteringAndNavigation(t *testing.T) {
	app := NewApp(nil, nil, 1*time.Second)
	view := sampleSystemView()
	_, _ = app.Update(SnapshotMsg{View: view})

	// Enter filter mode with '/'
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !app.filterMode {
		t.Fatalf("expected filterMode to be true")
	}

	// Type "nginx"
	for _, r := range "nginx" {
		_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if app.filterText != "nginx" {
		t.Errorf("expected filterText 'nginx', got %q", app.filterText)
	}
	if len(app.filteredProcs) != 1 || app.filteredProcs[0].Comm != "nginx" {
		t.Fatalf("expected only nginx in filtered procs, got %d items", len(app.filteredProcs))
	}

	// Hit Enter to exit filter mode
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app.filterMode {
		t.Errorf("expected filterMode false after Enter")
	}
	if len(app.filteredProcs) != 1 {
		t.Errorf("filter should remain active after Enter")
	}

	// Re-enter filter and hit Backspace 5 times
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	for i := 0; i < 5; i++ {
		_, _ = app.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	if app.filterText != "" {
		t.Errorf("expected empty filterText after backspaces, got %q", app.filterText)
	}
	if len(app.filteredProcs) != 3 {
		t.Errorf("expected all 3 processes restored, got %d", len(app.filteredProcs))
	}

	// Hit Escape to cancel filter mode
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if app.filterMode {
		t.Errorf("expected filterMode false after Esc")
	}

	// Navigation: down and up
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyDown})
	if app.cursor != 1 {
		t.Errorf("expected cursor 1 after down arrow, got %d", app.cursor)
	}
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyUp})
	if app.cursor != 0 {
		t.Errorf("expected cursor 0 after up arrow, got %d", app.cursor)
	}

	// Quit with 'q'
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !app.quitting {
		t.Errorf("expected quitting true after 'q'")
	}
	if cmd == nil {
		t.Errorf("expected tea.Quit command after 'q'")
	}
}

func TestSparklineAndGaugeHelpers(t *testing.T) {
	gauge := RenderGauge(50.0, 10)
	if !strings.Contains(gauge, "50.0%") {
		t.Errorf("expected gauge to contain '50.0%%', got %q", gauge)
	}

	spark := RenderSparkline([]float64{0, 25, 50, 75, 100}, 100.0, 5)
	if len([]rune(spark)) != 5 {
		t.Errorf("expected 5 runes in sparkline, got %d", len([]rune(spark)))
	}

	fb := FormatBytes(1024 * 1024 * 1500) // ~1.5 GB
	if !strings.Contains(fb, "1.5 GB") {
		t.Errorf("expected FormatBytes to return ~1.5 GB, got %s", fb)
	}
}

func TestFilterUnicodeAndInterrupt(t *testing.T) {
	app := NewApp(nil, nil, time.Second)
	app.filterMode = true
	app.filterText = "监测"
	app.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if app.filterText != "监" {
		t.Fatalf("invalid UTF-8 filter %q", app.filterText)
	}
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if !app.quitting || cmd == nil {
		t.Fatal("Ctrl+C ignored in filter mode")
	}
}
