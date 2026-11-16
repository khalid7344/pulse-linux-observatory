package alert

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

// alertTracker keeps track of consecutive breaches and firing state for a single alert key
type alertTracker struct {
	consecutiveBreaches int
	isFiring            bool
	firedAt             time.Time
}

// Evaluator evaluates alert rules on each SystemView snapshot using hysteresis.
// It implements model.MetricsSink.
type Evaluator struct {
	mu           sync.RWMutex
	cfg          model.AlertConfig
	trackers     map[string]*alertTracker
	activeAlerts map[string]model.AlertEvent
	history      *model.RingBuffer[model.AlertEvent]
	logFile      *os.File
	logger       *slog.Logger
}

// NewEvaluator creates a new alert Evaluator with the provided configuration.
func NewEvaluator(cfg model.AlertConfig) (*Evaluator, error) {
	if cfg.SustainedTicks <= 0 {
		cfg.SustainedTicks = 3
	}
	if cfg.CPUWarnPercent <= 0 {
		cfg.CPUWarnPercent = 85.0
	}
	if cfg.SysMemWarnPercent <= 0 {
		cfg.SysMemWarnPercent = 90.0
	}
	if cfg.MemWarnMB <= 0 {
		cfg.MemWarnMB = 2048
	}
	if cfg.ZombieWarnCount < 0 {
		cfg.ZombieWarnCount = 5
	}

	ev := &Evaluator{
		cfg:          cfg,
		trackers:     make(map[string]*alertTracker),
		activeAlerts: make(map[string]model.AlertEvent),
		history:      model.NewRingBuffer[model.AlertEvent](100),
	}

	if cfg.AlertLogPath != "" {
		f, err := os.OpenFile(cfg.AlertLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return nil, fmt.Errorf("open alert log file %q: %w", cfg.AlertLogPath, err)
		}
		ev.logFile = f
		ev.logger = slog.New(slog.NewJSONHandler(f, &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))
	}

	return ev, nil
}

// Name returns the sink identifier.
func (e *Evaluator) Name() string {
	return "alert-evaluator"
}

// ActiveAlerts returns a slice of currently FIRING alerts.
func (e *Evaluator) ActiveAlerts() []model.AlertEvent {
	e.mu.RLock()
	defer e.mu.RUnlock()

	res := make([]model.AlertEvent, 0, len(e.activeAlerts))
	for _, a := range e.activeAlerts {
		res = append(res, a)
	}
	return res
}

// RecentAlerts returns the chronological history of alert events (FIRING & RESOLVED).
func (e *Evaluator) RecentAlerts() []model.AlertEvent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.history.Slice()
}

// checkCondition evaluates a single rule condition with hysteresis
func (e *Evaluator) checkCondition(
	alertID string,
	ruleName string,
	severity string,
	conditionMet bool,
	val float64,
	thresh float64,
	pid int,
	comm string,
	msg string,
	now time.Time,
) {
	tracker, ok := e.trackers[alertID]
	if !ok {
		tracker = &alertTracker{}
		e.trackers[alertID] = tracker
	}

	if conditionMet {
		tracker.consecutiveBreaches++
		if tracker.consecutiveBreaches >= e.cfg.SustainedTicks {
			if !tracker.isFiring {
				// Transition to FIRING
				tracker.isFiring = true
				tracker.firedAt = now

				event := model.AlertEvent{
					ID:        alertID,
					RuleName:  ruleName,
					Severity:  severity,
					State:     "FIRING",
					Message:   msg,
					FiredAt:   now,
					Value:     val,
					Threshold: thresh,
					PID:       pid,
					Comm:      comm,
				}
				e.activeAlerts[alertID] = event
				e.history.Push(event)
				e.logAlert(event)
			}
		}
	} else {
		// Condition not met (dropped below or equal to threshold)
		if tracker.isFiring {
			// Transition to RESOLVED
			tracker.isFiring = false
			resolvedAt := now
			event := model.AlertEvent{
				ID:         alertID,
				RuleName:   ruleName,
				Severity:   severity,
				State:      "RESOLVED",
				Message:    fmt.Sprintf("%s resolved (value %.1f below threshold %.1f)", ruleName, val, thresh),
				FiredAt:    tracker.firedAt,
				ResolvedAt: &resolvedAt,
				Value:      val,
				Threshold:  thresh,
				PID:        pid,
				Comm:       comm,
			}
			delete(e.activeAlerts, alertID)
			e.history.Push(event)
			e.logAlert(event)
		}
		tracker.consecutiveBreaches = 0
	}
}

// logAlert writes structured JSON to log file if enabled
func (e *Evaluator) logAlert(event model.AlertEvent) {
	if e.logger == nil {
		return
	}

	attrs := []any{
		slog.String("alert_id", event.ID),
		slog.String("rule", event.RuleName),
		slog.String("severity", event.Severity),
		slog.String("state", event.State),
		slog.Float64("value", event.Value),
		slog.Float64("threshold", event.Threshold),
		slog.Time("fired_at", event.FiredAt),
	}
	if event.ResolvedAt != nil {
		attrs = append(attrs, slog.Time("resolved_at", *event.ResolvedAt))
	}
	if event.PID > 0 {
		attrs = append(attrs, slog.Int("pid", event.PID), slog.String("comm", event.Comm))
	}

	if event.State == "FIRING" {
		e.logger.Warn(event.Message, attrs...)
	} else {
		e.logger.Info(event.Message, attrs...)
	}
}

// OnSnapshot evaluates all configured threshold rules against the current SystemView.
func (e *Evaluator) OnSnapshot(ctx context.Context, view model.SystemView) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := view.Timestamp
	if now.IsZero() {
		now = time.Now()
	}

	// 1. System CPU High Rule
	e.checkCondition(
		"sys_cpu_high",
		"SystemCPUHigh",
		"WARNING",
		view.CPUPercent > e.cfg.CPUWarnPercent,
		view.CPUPercent,
		e.cfg.CPUWarnPercent,
		0,
		"",
		fmt.Sprintf("System CPU is high: %.1f%% (threshold %.1f%%)", view.CPUPercent, e.cfg.CPUWarnPercent),
		now,
	)

	// 2. System Memory High Rule
	e.checkCondition(
		"sys_mem_high",
		"SystemMemoryHigh",
		"WARNING",
		view.MemPercent > e.cfg.SysMemWarnPercent,
		view.MemPercent,
		e.cfg.SysMemWarnPercent,
		0,
		"",
		fmt.Sprintf("System Memory is high: %.1f%% (threshold %.1f%%)", view.MemPercent, e.cfg.SysMemWarnPercent),
		now,
	)

	// 3. Zombie Count Rule
	e.checkCondition(
		"zombie_count_high",
		"ZombieCountHigh",
		"WARNING",
		view.ZombieCount > e.cfg.ZombieWarnCount,
		float64(view.ZombieCount),
		float64(e.cfg.ZombieWarnCount),
		0,
		"",
		fmt.Sprintf("High zombie process count: %d (threshold %d)", view.ZombieCount, e.cfg.ZombieWarnCount),
		now,
	)

	// Track active process keys to prune dead processes
	activeKeys := make(map[string]bool)

	// 4 & 5. Per-Process Rules: CPU% High & RSS MB High
	for _, proc := range view.Processes {
		procID := fmt.Sprintf("proc_%d_%d", proc.Key.PID, proc.Key.StartTime)
		cpuAlertID := procID + "_cpu"
		rssAlertID := procID + "_rss"
		activeKeys[cpuAlertID] = true
		activeKeys[rssAlertID] = true

		// Process CPU High (Normalized CPU%)
		e.checkCondition(
			cpuAlertID,
			"ProcessCPUHigh",
			"WARNING",
			proc.CPUPercentNorm > e.cfg.CPUWarnPercent,
			proc.CPUPercentNorm,
			e.cfg.CPUWarnPercent,
			proc.Key.PID,
			proc.Comm,
			fmt.Sprintf("Process %s (PID %d) CPU is high: %.1f%% (threshold %.1f%%)", proc.Comm, proc.Key.PID, proc.CPUPercentNorm, e.cfg.CPUWarnPercent),
			now,
		)

		// Process RSS MB High
		rssMB := float64(proc.RSSBytes) / (1024 * 1024)
		e.checkCondition(
			rssAlertID,
			"ProcessRSSHigh",
			"WARNING",
			rssMB > float64(e.cfg.MemWarnMB),
			rssMB,
			float64(e.cfg.MemWarnMB),
			proc.Key.PID,
			proc.Comm,
			fmt.Sprintf("Process %s (PID %d) RSS is high: %.1f MB (threshold %d MB)", proc.Comm, proc.Key.PID, rssMB, e.cfg.MemWarnMB),
			now,
		)
	}

	// Prune process trackers for terminated/reused PIDs
	for id, tracker := range e.trackers {
		if id == "sys_cpu_high" || id == "sys_mem_high" || id == "zombie_count_high" {
			continue
		}
		if !activeKeys[id] {
			// Process terminated
			if tracker.isFiring {
				resolvedAt := now
				event := model.AlertEvent{
					ID:         id,
					RuleName:   "ProcessTerminated",
					Severity:   "INFO",
					State:      "RESOLVED",
					Message:    fmt.Sprintf("Process for alert %s terminated", id),
					FiredAt:    tracker.firedAt,
					ResolvedAt: &resolvedAt,
				}
				delete(e.activeAlerts, id)
				e.history.Push(event)
				e.logAlert(event)
			}
			delete(e.trackers, id)
		}
	}

	return nil
}

// Close closes the alert log file if open.
func (e *Evaluator) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.logFile != nil {
		err := e.logFile.Close()
		e.logFile = nil
		return err
	}
	return nil
}
