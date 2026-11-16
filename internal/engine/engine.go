package engine

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

// Engine coordinates telemetry collection, two-sample delta computation,
// bounded history retention, and distribution to MetricsSink consumers.
type Engine struct {
	mu            sync.RWMutex
	collector     model.Collector
	cfg           model.EngineConfig
	prevSample    *model.RawSystemSample
	prevProcMap   map[model.ProcessKey]model.RawProcessSample
	history       *model.RingBuffer[model.SystemHistoryPoint]
	procHistories map[model.ProcessKey]*model.RingBuffer[float64]
	sinks         []model.MetricsSink
	tickCount     uint64
	lastView      *model.SystemView
}

// NewEngine creates a new Engine configured with the given collector and settings.
func NewEngine(collector model.Collector, cfg model.EngineConfig) *Engine {
	if cfg.HistorySize <= 0 {
		cfg.HistorySize = 300
	}
	if cfg.UserHZ == 0 {
		cfg.UserHZ = 100
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 1 * time.Second
	}

	return &Engine{
		collector:     collector,
		cfg:           cfg,
		prevProcMap:   make(map[model.ProcessKey]model.RawProcessSample),
		history:       model.NewRingBuffer[model.SystemHistoryPoint](cfg.HistorySize),
		procHistories: make(map[model.ProcessKey]*model.RingBuffer[float64]),
		sinks:         make([]model.MetricsSink, 0),
	}
}

// RegisterSink adds a downstream telemetry consumer. Thread-safe.
func (e *Engine) RegisterSink(sink model.MetricsSink) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.sinks = append(e.sinks, sink)
}

// UnregisterSink removes a sink by name. Thread-safe.
func (e *Engine) UnregisterSink(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, s := range e.sinks {
		if s.Name() == name {
			e.sinks = append(e.sinks[:i], e.sinks[i+1:]...)
			return
		}
	}
}

// SetIrixMode toggles the CPU% calculation mode at runtime.
func (e *Engine) SetIrixMode(enabled bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg.IrixMode = enabled
}

// IrixMode returns the current CPU% calculation mode.
func (e *Engine) IrixMode() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg.IrixMode
}

// Config returns a copy of the current engine config.
func (e *Engine) Config() model.EngineConfig {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cfg
}

// LastView returns the most recently computed SystemView snapshot, or nil if no ticks yet.
func (e *Engine) LastView() *model.SystemView {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.lastView == nil {
		return nil
	}
	view := cloneView(*e.lastView)
	return &view
}

// Tick executes a single telemetry collection pass, calculates two-sample deltas,
// updates historical buffers, and dispatches the immutable SystemView to all registered sinks.
func (e *Engine) Tick(ctx context.Context) (model.SystemView, error) {
	scanStart := time.Now()
	rawSample, err := e.collector.Collect(ctx)
	if err != nil {
		return model.SystemView{}, err
	}
	scanDuration := time.Since(scanStart)

	return e.ProcessSample(ctx, rawSample, scanDuration)
}

// ProcessSample computes deltas from rawSample, updating engine state.
// This separation allows deterministic unit tests to supply custom RawSystemSamples.
func (e *Engine) ProcessSample(ctx context.Context, curr model.RawSystemSample, scanDuration time.Duration) (model.SystemView, error) {
	e.mu.Lock()

	e.tickCount++
	tickNumber := e.tickCount

	// Determine elapsed time and total system jiffy delta
	var totalDeltaJiffies uint64
	var activeDeltaJiffies uint64
	var elapsedSeconds float64
	var sysCPUPercent float64

	prev := e.prevSample
	if prev != nil {
		if curr.Timestamp.After(prev.Timestamp) {
			elapsedSeconds = curr.Timestamp.Sub(prev.Timestamp).Seconds()
		} else {
			elapsedSeconds = e.cfg.Interval.Seconds()
		}

		if curr.TotalJiffies > prev.TotalJiffies {
			totalDeltaJiffies = curr.TotalJiffies - prev.TotalJiffies
		}
		if curr.ActiveJiffies > prev.ActiveJiffies {
			activeDeltaJiffies = curr.ActiveJiffies - prev.ActiveJiffies
		}

		if totalDeltaJiffies > 0 {
			sysCPUPercent = (float64(activeDeltaJiffies) / float64(totalDeltaJiffies)) * 100.0
			if sysCPUPercent < 0 {
				sysCPUPercent = 0.0
			} else if sysCPUPercent > 100.0 {
				sysCPUPercent = 100.0
			}
		}
	} else {
		// First tick has no baseline sample
		elapsedSeconds = e.cfg.Interval.Seconds()
	}

	if elapsedSeconds <= 0 {
		elapsedSeconds = e.cfg.Interval.Seconds()
	}

	numCores := curr.NumCores
	if numCores <= 0 {
		numCores = 1
	}

	// Calculate system memory percentage
	var sysMemPercent float64
	if curr.MemTotalBytes > 0 {
		sysMemPercent = (float64(curr.MemUsedBytes) / float64(curr.MemTotalBytes)) * 100.0
		if sysMemPercent > 100.0 {
			sysMemPercent = 100.0
		}
	}

	// Build map of current processes and calculate per-process metrics
	currProcMap := make(map[model.ProcessKey]model.RawProcessSample, len(curr.Processes))
	procMetrics := make([]model.ProcessMetrics, 0, len(curr.Processes))

	var zombieCount int
	var runningCount int

	for _, proc := range curr.Processes {
		currProcMap[proc.Key] = proc

		if proc.State == 'Z' {
			zombieCount++
		} else if proc.State == 'R' {
			runningCount++
		}

		var cpuNorm float64
		var cpuIrix float64

		// Check for previous process sample with identical ProcessKey (PID + StartTime)
		if prevProc, found := e.prevProcMap[proc.Key]; found && prev != nil {
			prevProcJiffies := prevProc.UTimeJiffies + prevProc.STimeJiffies
			currProcJiffies := proc.UTimeJiffies + proc.STimeJiffies

			var deltaProcJiffies uint64
			if currProcJiffies > prevProcJiffies {
				deltaProcJiffies = currProcJiffies - prevProcJiffies
			}

			// (a) Normalized CPU%: (deltaProc / totalSystemDelta) * 100
			if totalDeltaJiffies > 0 {
				cpuNorm = (float64(deltaProcJiffies) / float64(totalDeltaJiffies)) * 100.0
				if cpuNorm < 0 {
					cpuNorm = 0.0
				} else if cpuNorm > 100.0 {
					cpuNorm = 100.0
				}
			}

			// (b) Irix CPU%: (deltaProc / (elapsedSeconds * USER_HZ)) * 100
			denom := elapsedSeconds * float64(e.cfg.UserHZ)
			if denom > 0 {
				cpuIrix = (float64(deltaProcJiffies) / denom) * 100.0
				if cpuIrix < 0 {
					cpuIrix = 0.0
				}
				maxIrix := 100.0 * float64(numCores)
				if cpuIrix > maxIrix {
					cpuIrix = maxIrix
				}
			}
		} else {
			// Process is new or PID was recycled (different StartTime):
			// Initialize delta CPU% to 0.0% as specified in FR-03.
			cpuNorm = 0.0
			cpuIrix = 0.0
		}

		var memPercent float64
		if curr.MemTotalBytes > 0 {
			memPercent = (float64(proc.RSSBytes) / float64(curr.MemTotalBytes)) * 100.0
			if memPercent > 100.0 {
				memPercent = 100.0
			}
		}

		// Update or allocate per-process CPU ring history
		var pHist *model.RingBuffer[float64]
		var ok bool
		if pHist, ok = e.procHistories[proc.Key]; !ok {
			pHist = model.NewRingBuffer[float64](e.cfg.HistorySize) // Keep last 60 ticks for process trend
			e.procHistories[proc.Key] = pHist
		}
		pHist.Push(cpuNorm)

		metrics := model.ProcessMetrics{
			Key:            proc.Key,
			PPID:           proc.PPID,
			UID:            proc.UID,
			Comm:           proc.Comm,
			Cmdline:        proc.Cmdline,
			State:          string(proc.State),
			CPUPercentNorm: roundFloat(cpuNorm, 2),
			CPUPercentIrix: roundFloat(cpuIrix, 2),
			MemPercent:     roundFloat(memPercent, 2),
			VMSBytes:       proc.VMSBytes,
			RSSBytes:       proc.RSSBytes,
			PSSBytes:       proc.PSSBytes,
			PSSValid:       proc.PSSValid,
			NumThreads:     proc.NumThreads,
		}
		procMetrics = append(procMetrics, metrics)
	}

	// Dead-PID pruning: remove processes from procHistories that no longer exist
	for key := range e.procHistories {
		if _, exists := currProcMap[key]; !exists {
			delete(e.procHistories, key)
		}
	}

	// Update system-level historical ring buffer
	historyPoint := model.SystemHistoryPoint{
		Timestamp:   curr.Timestamp,
		CPUPercent:  roundFloat(sysCPUPercent, 2),
		MemPercent:  roundFloat(sysMemPercent, 2),
		TotalProcs:  len(curr.Processes),
		ZombieCount: zombieCount,
	}
	e.history.Push(historyPoint)

	// Save current state for next tick
	sampleCopy := curr
	e.prevSample = &sampleCopy
	e.prevProcMap = currProcMap

	view := model.SystemView{
		TickNumber:        tickNumber,
		Timestamp:         curr.Timestamp,
		SampleInterval:    time.Duration(elapsedSeconds * float64(time.Second)),
		NumCores:          numCores,
		UserHZ:            e.cfg.UserHZ,
		CPUPercent:        roundFloat(sysCPUPercent, 2),
		MemTotalBytes:     curr.MemTotalBytes,
		MemUsedBytes:      curr.MemUsedBytes,
		MemAvailableBytes: curr.MemAvailableBytes,
		MemPercent:        roundFloat(sysMemPercent, 2),
		SwapTotalBytes:    curr.SwapTotalBytes,
		SwapUsedBytes:     curr.SwapUsedBytes,
		TotalProcs:        len(curr.Processes),
		RunningProcs:      runningCount,
		ZombieCount:       zombieCount,
		SkippedVanished:   curr.SkippedVanished,
		SkippedPermission: curr.SkippedPermission,
		ParseErrors:       curr.ParseErrors,
		ScanDuration:      scanDuration,
		Processes:         procMetrics,
		History:           e.history.Slice(),
		ActiveAlerts:      nil, // Sinks (such as AlertEvaluator) inspect or append alerts
	}

	stored := cloneView(view)
	e.lastView = &stored

	// Dispatch snapshot to registered MetricsSink consumers
	sinksSnapshot := make([]model.MetricsSink, len(e.sinks))
	copy(sinksSnapshot, e.sinks)

	e.mu.Unlock()

	for _, s := range sinksSnapshot {
		if err := s.OnSnapshot(ctx, cloneView(view)); err != nil {
			slog.Warn("metrics sink failed", "sink", s.Name(), "error", err)
		}
	}

	return view, nil
}

// Close closes all registered sinks.
func (e *Engine) Close() error {
	e.mu.Lock()
	sinks := append([]model.MetricsSink(nil), e.sinks...)
	e.sinks = nil
	e.mu.Unlock()
	var firstErr error
	for _, s := range sinks {
		if err := s.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Helper function to round float to specific decimal places
func roundFloat(val float64, precision int) float64 {
	ratio := math.Pow(10, float64(precision))
	return math.Round(val*ratio) / ratio
}

// cloneView isolates slices (and optional alert timestamps) from callers and sinks.
func cloneView(v model.SystemView) model.SystemView {
	v.Processes = append([]model.ProcessMetrics(nil), v.Processes...)
	v.History = append([]model.SystemHistoryPoint(nil), v.History...)
	v.ActiveAlerts = append([]model.AlertEvent(nil), v.ActiveAlerts...)
	for i := range v.ActiveAlerts {
		if v.ActiveAlerts[i].ResolvedAt != nil {
			t := *v.ActiveAlerts[i].ResolvedAt
			v.ActiveAlerts[i].ResolvedAt = &t
		}
	}
	return v
}
