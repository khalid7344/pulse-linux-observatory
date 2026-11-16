package model

import "context"

// Collector abstracts /proc reading for deterministic testing against io/fs.FS fixtures.
type Collector interface {
	Collect(ctx context.Context) (RawSystemSample, error)
}

// MetricsSink defines the extension boundary for downstream telemetry consumers.
// In v1.0, internal/alert.Evaluator implements MetricsSink.
// In v1.1 (Future Phase 2), internal/exporter/prom.Exporter will implement MetricsSink
// without modifying internal/procfs or internal/engine.
type MetricsSink interface {
	Name() string
	OnSnapshot(ctx context.Context, view SystemView) error
	Close() error
}
