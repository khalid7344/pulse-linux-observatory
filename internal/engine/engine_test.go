package engine

import (
	"context"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/khalid7344/pulse-linux-observatory/internal/model"
	"github.com/khalid7344/pulse-linux-observatory/internal/procfs"
)

// mockSink implements model.MetricsSink for testing
type mockSink struct {
	mu        sync.Mutex
	name      string
	snapshots []model.SystemView
	closed    bool
}

func newMockSink(name string) *mockSink {
	return &mockSink{name: name}
}

func (m *mockSink) Name() string {
	return m.name
}

func (m *mockSink) OnSnapshot(ctx context.Context, view model.SystemView) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots = append(m.snapshots, view)
	return nil
}

func (m *mockSink) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockSink) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.snapshots)
}

func almostEqual(a, b, epsilon float64) bool {
	return math.Abs(a-b) <= epsilon
}

func TestEngine_Tick01ToTick02_DeterministicDeltas(t *testing.T) {
	// Setup collectors pointing at deterministic fixtures
	fs01 := os.DirFS("../../testdata/proc_fixtures/tick_01")
	c01 := procfs.NewFSCollector(fs01, 4096, true)

	fs02 := os.DirFS("../../testdata/proc_fixtures/tick_02")
	c02 := procfs.NewFSCollector(fs02, 4096, true)

	ctx := context.Background()

	s01, err := c01.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect tick_01 failed: %v", err)
	}
	s01.Timestamp = time.Unix(1000, 0) // T = 0s

	s02, err := c02.Collect(ctx)
	if err != nil {
		t.Fatalf("Collect tick_02 failed: %v", err)
	}
	s02.Timestamp = time.Unix(1001, 0) // T = 1s, Delta t = 1.0s

	cfg := model.EngineConfig{
		Interval:    1 * time.Second,
		HistorySize: 300,
		UserHZ:      100,
		IrixMode:    false,
		PageSize:    4096,
	}

	eng := NewEngine(c01, cfg)
	sink := newMockSink("test-sink")
	eng.RegisterSink(sink)

	// First tick (Baseline)
	view1, err := eng.ProcessSample(ctx, s01, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("ProcessSample tick 1 failed: %v", err)
	}
	if view1.TickNumber != 1 {
		t.Errorf("expected tick 1, got %d", view1.TickNumber)
	}
	if view1.CPUPercent != 0.0 {
		t.Errorf("expected 0.0%% CPU on first tick baseline, got %f", view1.CPUPercent)
	}

	// Second tick
	view2, err := eng.ProcessSample(ctx, s02, 1*time.Millisecond)
	if err != nil {
		t.Fatalf("ProcessSample tick 2 failed: %v", err)
	}
	if view2.TickNumber != 2 {
		t.Errorf("expected tick 2, got %d", view2.TickNumber)
	}

	// 1. System CPU% Assert:
	// Total delta = 400, Active delta = 200 => 50.0%
	if !almostEqual(view2.CPUPercent, 50.0, 1e-2) {
		t.Errorf("expected System CPU 50.0%%, got %f%%", view2.CPUPercent)
	}

	// Find processes in view2
	procMap := make(map[int]model.ProcessMetrics)
	for _, p := range view2.Processes {
		procMap[p.Key.PID] = p
	}

	// 2. PID 101 (nginx):
	// proc delta = 20 jiffies
	// Normalized = 20 / 400 * 100 = 5.0%
	// Irix = 20 / (1.0 * 100) * 100 = 20.0%
	p101, ok := procMap[101]
	if !ok {
		t.Fatalf("PID 101 missing in tick 2")
	}
	if !almostEqual(p101.CPUPercentNorm, 5.0, 1e-2) {
		t.Errorf("PID 101: expected 5.0%% normalized CPU, got %f%%", p101.CPUPercentNorm)
	}
	if !almostEqual(p101.CPUPercentIrix, 20.0, 1e-2) {
		t.Errorf("PID 101: expected 20.0%% Irix CPU, got %f%%", p101.CPUPercentIrix)
	}

	// 3. PID 202 (Web Content (1)):
	// proc delta = 180 jiffies
	// Normalized = 180 / 400 * 100 = 45.0%
	// Irix = 180 / (1.0 * 100) * 100 = 180.0%
	p202, ok := procMap[202]
	if !ok {
		t.Fatalf("PID 202 missing in tick 2")
	}
	if !almostEqual(p202.CPUPercentNorm, 45.0, 1e-2) {
		t.Errorf("PID 202: expected 45.0%% normalized CPU, got %f%%", p202.CPUPercentNorm)
	}
	if !almostEqual(p202.CPUPercentIrix, 180.0, 1e-2) {
		t.Errorf("PID 202: expected 180.0%% Irix CPU, got %f%%", p202.CPUPercentIrix)
	}

	// 4. PID 303 (PID REUSE! StartTime changed 7000 -> 7095):
	// Must be reset to 0.0% (FR-03)
	p303, ok := procMap[303]
	if !ok {
		t.Fatalf("PID 303 missing in tick 2")
	}
	if p303.Key.StartTime != 7095 {
		t.Errorf("expected StartTime 7095, got %d", p303.Key.StartTime)
	}
	if p303.CPUPercentNorm != 0.0 {
		t.Errorf("PID 303 reuse: expected 0.0%% normalized CPU, got %f%%", p303.CPUPercentNorm)
	}
	if p303.CPUPercentIrix != 0.0 {
		t.Errorf("PID 303 reuse: expected 0.0%% Irix CPU, got %f%%", p303.CPUPercentIrix)
	}

	// 5. PID 404 (Zombie process):
	p404, ok := procMap[404]
	if !ok {
		t.Fatalf("PID 404 missing in tick 2")
	}
	if p404.State != "Z" {
		t.Errorf("expected PID 404 state 'Z', got %q", p404.State)
	}
	if view2.ZombieCount != 1 {
		t.Errorf("expected ZombieCount 1, got %d", view2.ZombieCount)
	}

	// 6. Sinks count:
	if sink.Count() != 2 {
		t.Errorf("expected sink to receive 2 snapshots, got %d", sink.Count())
	}

	// 7. Close engine:
	if err := eng.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
	if !sink.closed {
		t.Errorf("expected sink to be closed")
	}
}

func TestEngine_CounterRegressionClamping(t *testing.T) {
	cfg := model.EngineConfig{
		Interval: 1 * time.Second,
		UserHZ:   100,
	}
	eng := NewEngine(nil, cfg)
	ctx := context.Background()

	t0 := time.Now()
	sample1 := model.RawSystemSample{
		Timestamp:     t0,
		TotalJiffies:  1000,
		ActiveJiffies: 500,
		Processes: []model.RawProcessSample{
			{
				Key:          model.ProcessKey{PID: 10, StartTime: 100},
				UTimeJiffies: 100,
				STimeJiffies: 50,
			},
		},
	}
	_, _ = eng.ProcessSample(ctx, sample1, time.Millisecond)

	// Sample 2 with wrapped/regressed counters (e.g. CPU or process reset)
	sample2 := model.RawSystemSample{
		Timestamp:     t0.Add(1 * time.Second),
		TotalJiffies:  900, // decreased
		ActiveJiffies: 400, // decreased
		Processes: []model.RawProcessSample{
			{
				Key:          model.ProcessKey{PID: 10, StartTime: 100},
				UTimeJiffies: 50, // decreased
				STimeJiffies: 20,
			},
		},
	}

	view2, err := eng.ProcessSample(ctx, sample2, time.Millisecond)
	if err != nil {
		t.Fatalf("ProcessSample failed: %v", err)
	}

	if view2.CPUPercent != 0.0 {
		t.Errorf("expected 0.0%% system CPU on counter regression, got %f", view2.CPUPercent)
	}
	if len(view2.Processes) != 1 || view2.Processes[0].CPUPercentNorm != 0.0 {
		t.Errorf("expected 0.0%% process CPU on counter regression, got %f", view2.Processes[0].CPUPercentNorm)
	}
}

func TestEngine_DeadPIDPruning(t *testing.T) {
	cfg := model.EngineConfig{
		Interval: 1 * time.Second,
		UserHZ:   100,
	}
	eng := NewEngine(nil, cfg)
	ctx := context.Background()

	t0 := time.Now()
	sample1 := model.RawSystemSample{
		Timestamp:    t0,
		TotalJiffies: 1000,
		Processes: []model.RawProcessSample{
			{Key: model.ProcessKey{PID: 1, StartTime: 100}},
			{Key: model.ProcessKey{PID: 2, StartTime: 100}},
		},
	}
	_, _ = eng.ProcessSample(ctx, sample1, time.Millisecond)

	if len(eng.procHistories) != 2 {
		t.Fatalf("expected 2 procHistories, got %d", len(eng.procHistories))
	}

	// Sample 2: PID 2 disappeared
	sample2 := model.RawSystemSample{
		Timestamp:    t0.Add(1 * time.Second),
		TotalJiffies: 1100,
		Processes: []model.RawProcessSample{
			{Key: model.ProcessKey{PID: 1, StartTime: 100}},
		},
	}
	_, _ = eng.ProcessSample(ctx, sample2, time.Millisecond)

	// PID 2 history should be pruned
	if len(eng.procHistories) != 1 {
		t.Errorf("expected 1 procHistory after pruning, got %d", len(eng.procHistories))
	}
	if _, ok := eng.procHistories[model.ProcessKey{PID: 1, StartTime: 100}]; !ok {
		t.Errorf("expected PID 1 history to be retained")
	}
}

func TestEngine_ConcurrentSinksAndRace(t *testing.T) {
	cfg := model.EngineConfig{
		Interval:    10 * time.Millisecond,
		HistorySize: 50,
		UserHZ:      100,
	}
	eng := NewEngine(nil, cfg)
	ctx := context.Background()

	sinks := []*mockSink{
		newMockSink("sink1"),
		newMockSink("sink2"),
		newMockSink("sink3"),
	}
	for _, s := range sinks {
		eng.RegisterSink(s)
	}

	var wg sync.WaitGroup
	// Concurrently read views and toggle modes
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = eng.LastView()
			eng.SetIrixMode(i%2 == 0)
			_ = eng.IrixMode()
			time.Sleep(1 * time.Millisecond)
		}
	}()

	// Feed samples concurrently
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			s := model.RawSystemSample{
				Timestamp:    time.Now(),
				TotalJiffies: uint64(1000 + i*100),
				NumCores:     4,
				Processes: []model.RawProcessSample{
					{
						Key:          model.ProcessKey{PID: 1, StartTime: 100},
						UTimeJiffies: uint64(100 + i*10),
					},
				},
			}
			_, _ = eng.ProcessSample(ctx, s, time.Millisecond)
			time.Sleep(1 * time.Millisecond)
		}
	}()

	wg.Wait()
	_ = eng.Close()
}

func BenchmarkEngine_Tick(b *testing.B) {
	cfg := model.EngineConfig{
		Interval:    1 * time.Second,
		HistorySize: 300,
		UserHZ:      100,
	}
	eng := NewEngine(nil, cfg)
	ctx := context.Background()

	// 300 processes
	procs := make([]model.RawProcessSample, 300)
	for i := 0; i < 300; i++ {
		procs[i] = model.RawProcessSample{
			Key:          model.ProcessKey{PID: 1000 + i, StartTime: 5000},
			Comm:         fmt.Sprintf("worker-%d", i),
			UTimeJiffies: uint64(100 + i),
			STimeJiffies: 50,
			State:        'S',
			VMSBytes:     104857600,
			RSSBytes:     20971520,
		}
	}

	sample := model.RawSystemSample{
		Timestamp:    time.Now(),
		TotalJiffies: 100000,
		NumCores:     4,
		Processes:    procs,
	}

	// Warmup
	_, _ = eng.ProcessSample(ctx, sample, time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sample.Timestamp = sample.Timestamp.Add(1 * time.Second)
		sample.TotalJiffies += 400
		for j := range sample.Processes {
			sample.Processes[j].UTimeJiffies += 1
		}
		_, err := eng.ProcessSample(ctx, sample, time.Millisecond)
		if err != nil {
			b.Fatalf("ProcessSample failed: %v", err)
		}
	}
}

type reentrantSink struct{ eng *Engine }

func (s reentrantSink) Name() string { return "reentrant" }
func (s reentrantSink) Close() error { return nil }
func (s reentrantSink) OnSnapshot(_ context.Context, v model.SystemView) error {
	_ = s.eng.Config()
	_ = s.eng.LastView()
	v.Processes[0].Comm = "sink mutation"
	return nil
}

func TestSnapshotsAreIsolatedAndSinkCanReadEngine(t *testing.T) {
	eng := NewEngine(nil, model.DefaultConfig().Engine)
	eng.RegisterSink(reentrantSink{eng})
	sample := model.RawSystemSample{Timestamp: time.Now(), Processes: []model.RawProcessSample{{Key: model.ProcessKey{PID: 1, StartTime: 10}, Comm: "original"}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		view, err := eng.ProcessSample(context.Background(), sample, 0)
		if err != nil {
			t.Error(err)
			return
		}
		view.Processes[0].Comm = "caller mutation"
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sink deadlocked reading engine")
	}
	v := eng.LastView()
	if v.Processes[0].Comm != "original" {
		t.Fatal("snapshot leaked mutation")
	}
	v.Processes[0].Comm = "reader mutation"
	if eng.LastView().Processes[0].Comm != "original" {
		t.Fatal("LastView exposed internal state")
	}
}
