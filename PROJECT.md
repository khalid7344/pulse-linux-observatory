# Pulse Linux Observatory — System Specification

Status: IMPLEMENTED; see AUDIT.md for current verification and limitations.

## 1. Overview & Problem Statement

**Core Problem**
Existing Linux process and system monitors either rely on heavy external dependency chains (such as `gopsutil` or C-linked wrappers) or prioritize sprawling visual interfaces (`btop`, `htop`) at the expense of deterministic testability, low runtime overhead, and historical alert forensics. Furthermore, naive process monitors frequently misidentify processes across ticks when the Linux kernel recycles Process IDs (`PID` reuse), and misrepresent memory pressure by conflating Virtual Memory Size (`VMS`) and Resident Set Size (`RSS`—which double-counts shared libraries across forked processes) without accounting for Proportional Set Size (`PSS`) or shared-memory limitations.

**Target Users**
* Linux systems engineers and performance-conscious developers operating on Pop!_OS 24.04 (COSMIC Wayland / `zsh`) and modern Linux distributions.
* Technical evaluators and academic reviewers inspecting production-grade, systems-level Go software engineering with deterministic verification.

**Primary Use Cases**
* **Live Interactive Process & System Monitoring:** Real-time inspection of total system CPU/memory pressure and per-process telemetry in a responsive Terminal UI (TUI) with sorting, filtering, and sparkline trends.
* **Sustained Anomaly Detection & Alerting:** Automated evaluation of configurable threshold rules (CPU%, RSS MB, and Zombie process counts) with hysteresis (`sustained_ticks`) to log transient spikes and sustained resource leaks to an in-memory alert drawer and a structured JSONL audit log.
* **Headless & Deterministic Verification:** Single-pass (`--once`) or bounded-tick (`--headless`) CLI execution against either the live `/proc` filesystem or synthetic `/proc` fixture trees for automated regression testing and benchmarking.

**Expected Outcome**
**Pulse Linux Observatory** (`pulse`): A single-binary, `CGO_ENABLED=0` Go telemetry engine and TUI that parses `/proc` directly through Go's `io/fs.FS` abstraction. It tracks process identity using composite keys (`PID + StartTime`), calculates accurate two-sample CPU deltas, distinguishes `RSS`, `VMS`, and optional `PSS` memory metrics, retains bounded in-memory historical ring buffers, evaluates hysteresis-backed alerts, and defines a clean exporter boundary for future Prometheus integration.

---

## 2. Goals & Non-Goals (Out of Scope)

### Concrete MVP Goals
* **G1 — Direct, Dependency-Free `/proc` Parsing:** Parse `/proc/stat`, `/proc/meminfo`, `/proc/[pid]/stat`, `/proc/[pid]/status`, `/proc/[pid]/cmdline`, and optional `/proc/[pid]/smaps_rollup` using only the Go standard library and an injected `fs.FS` reader.
* **G2 — PID Reuse Safety:** Uniquely identify every process across collection ticks using the composite key `ProcessKey{PID: int, StartTime: uint64}` (where `StartTime` is field 22 `starttime` in clock ticks since boot from `/proc/[pid]/stat`), preventing false CPU delta spikes when a PID is recycled between ticks.
* **G3 — Accurate Memory Accounting (`RSS`, `VMS`, Optional `PSS`):** Clearly report `VMS` (`vsize`), `RSS` (`rss * page_size` or `VmRSS`), and optional `PSS` (`Pss` from `/proc/[pid]/smaps_rollup` when enabled/readable for top memory consumers), while explicitly documenting shared-library accounting trade-offs.
* **G4 — Bounded Historical Retention:** Maintain fixed-capacity circular ring buffers (`RingBuffer[T]`) for system-wide aggregates and active processes (default 300 ticks = 5 minutes at 1Hz) with $O(1)$ insertion and predictable memory ceilings.
* **G5 — Hysteresis Alerting & Structured Audit Trail:** Evaluate configurable thresholds with sustained tick requirements (`SustainedTicks`) to eliminate single-tick flapping, displaying active/resolved alerts in the TUI and optionally writing `log/slog` JSONL events to disk.
* **G6 — Interactive TUI & Headless Modes:** Provide a responsive Bubble Tea + Lip Gloss TUI alongside a deterministic `--headless` / `--once` execution mode.
* **G7 — Future Prometheus Architectural Contract:** Define and wire a zero-dependency `MetricsSink` interface in `internal/model` and document the Phase 2 Prometheus exporter design without implementing an HTTP server or adding Prometheus dependencies in v1.0.

### Explicit Non-Goals (Out of Scope for v1.0)
* **NG1 — Live Prometheus HTTP Endpoint:** Do not implement `/metrics` HTTP listeners or import `github.com/prometheus/client_golang` in Milestones M1–M5.
* **NG2 — Kernel Space / Root Requirements:** No eBPF probes, `perf_event_open` syscalls, netlink sockets, or mandatory `root` / `CAP_SYS_ADMIN` privileges.
* **NG3 — Non-Process Subsystem Telemetry:** No GPU telemetry (`nvidia-smi` / NVML), block device I/O accounting (`/proc/[pid]/io`), or network packet capture.
* **NG4 — External Databases or CGO:** No SQLite, `bbolt`, InfluxDB, or `CGO_ENABLED=1` system calls.

---

## 3. Architecture & Technology Stack (with Rationale)

### Technology Stack
* **Language & Runtime:** Go `1.24.0+` compiled with `CGO_ENABLED=0` for a static, portable Linux binary.
* **Standard Library Foundation:**
  * `io/fs` and `os.DirFS`: Abstracts `/proc` file access so unit tests and benchmarks run against deterministic fixture directories (`testdata/proc_fixtures/...`) without mocking OS syscalls.
  * `log/slog`: Structured JSON alert and diagnostic logging.
  * `encoding/json` and `flag`: Configuration file parsing and CLI flag handling without third-party Viper/Cobra bloat.
* **Allowed External Dependencies (Strictly Limited):**
  * `github.com/charmbracelet/bubbletea` (TUI Elm-architecture state loop).
  * `github.com/charmbracelet/lipgloss` (Declarative terminal layout and styling).
  * *(Indirect transitive dependencies required by Bubble Tea/Lip Gloss are permitted; no other direct third-party packages may be added to `go.mod`.)*

### Architectural Pattern
Hexagonal, single-binary event-driven pipeline divided into four internal domain layers coordinated via `context.Context`:

```text
+-----------------------------------------------------------------------+
|                           cmd/pulse/main.go                           |
|          CLI Flags, JSON Config Loader, Signal Handling (SIGINT)      |
+-----------------------------------+-----------------------------------+
                                    |
                                    v
+-----------------------------------------------------------------------+
|                        internal/procfs (Collector)                    |
|   Injected io/fs.FS -> Low-Alloc Byte Parsers (/proc/stat, meminfo,   |
|   [pid]/stat, [pid]/status, [pid]/cmdline, optional smaps_rollup)     |
+-----------------------------------+-----------------------------------+
                                    | RawTickSnapshot
                                    v
+-----------------------------------------------------------------------+
|                    internal/engine (State & History)                  |
|  - Matches processes via ProcessKey{PID, StartTime} (PID reuse safe)  |
|  - Computes CPU% deltas (Normalized 0-100% or Irix mode, USER_HZ)     |
|  - Updates bounded RingBuffer[SystemSample] & Process History         |
|  - Publishes immutable SystemView to registered MetricsSink list      |
+-----------------+-----------------------------------+-----------------+
                  |                                   |
                  v                                   v
+-----------------------------------+   +-------------------------------+
|    internal/alert (MetricsSink)   |   |  Future PromExporter (Stub)   |
|  - Hysteresis rule evaluation     |   |  - Documented Phase 2 sink    |
|  - Emits AlertEvent to TUI &      |   |    implementing MetricsSink   |
|    optional slog JSONL file       |   +-------------------------------+
+-----------------+-----------------+
                  | AlertEvent / SystemView
                  v
+-----------------------------------------------------------------------+
|                       internal/tui (Presentation)                     |
|   Bubble Tea Model/Update/View: Header Gauges, Sparklines, Sortable   |
|   & Filterable Process Table (RSS/VMS/PSS), Live Alert Banner Drawer  |
+-----------------------------------------------------------------------+

```

---

## 4. Functional Requirements (Prioritized P0/P1)

| ID | Priority | Requirement & Observable Expected Behavior |
| --- | --- | --- |
| **FR-01** | **P0** | **Global System Parsing (`/proc/stat` & `/proc/meminfo`):** Parse aggregate CPU jiffies (`user`, `nice`, `system`, `idle`, `iowait`, `irq`, `softirq`, `steal`) from the `cpu ` header line of `/proc/stat`, logical core count (`cpu0..cpuN`), and system memory metrics (`MemTotal`, `MemFree`, `MemAvailable`, `Buffers`, `Cached`, `SwapTotal`, `SwapFree`) from `/proc/meminfo`. Compute used memory as `MemTotal - MemAvailable` (falling back to `MemTotal - (MemFree + Buffers + Cached)` if `MemAvailable` is absent). |
| **FR-02** | **P0** | **Resilient Per-Process Parsing (`/proc/[pid]/*`):** Enumerate numeric directories in `/proc`. Parse `/proc/[pid]/stat` by locating the first `(` and the **last** `)` to safely extract `comm` names containing spaces, parentheses, or newlines (e.g., `(Web Content)` or `(kworker/u16:1)`). Extract `state` (field 3), `ppid` (field 4), `utime` (field 14), `stime` (field 15), `num_threads` (field 20), `starttime` (field 22), `vsize` (field 23), and `rss` pages (field 24). Parse `Uid` and `VmRSS` / `VmSize` from `/proc/[pid]/status` and NUL-delimited arguments from `/proc/[pid]/cmdline` (falling back to `[comm]` for kernel threads with empty `cmdline`). |
| **FR-03** | **P0** | **PID Reuse Detection via Composite Identity:** Every process must be keyed by `ProcessKey{PID: int, StartTime: uint64}`. If the same numeric `PID` appears in tick $T_2$ with a different `StartTime` than in tick $T_1$, the engine must treat it as a brand-new process (initializing delta CPU% to `0.0%` for its first tick and resetting its per-process history buffer) rather than subtracting old jiffies from the prior process. |
| **FR-04** | **P0** | **Memory Metric Distinction (`RSS`, `VMS`, Optional `PSS`):** Always populate `VMSBytes` (virtual address space) and `RSSBytes` (resident physical pages, including shared libraries). When `--enable-pss` is set (or for top-N memory processes when configured), attempt to read `/proc/[pid]/smaps_rollup` and parse `Pss:`. If unreadable (due to permissions or kernel support), set `PSSBytes = 0` and `PSSSupported = false` without error. Document clearly in UI/README that `RSS` overcounts shared memory (`libc`, IPC shm) whereas `PSS` divides shared pages proportionally. |
| **FR-05** | **P0** | **Two-Sample CPU Delta & Normalization Modes:** Compute system CPU% from total/active jiffy deltas between ticks. Compute per-process CPU% using both: (a) **Normalized Mode (default):** `100.0 * (procDeltaJiffies / totalSystemDeltaJiffies)` bounded to `[0.0, 100.0]`, and (b) **Irix Mode (`--irix` or `i` key):** `100.0 * (procDeltaJiffies / (elapsedSeconds * USER_HZ))` bounded to `[0.0, 100.0 * numCores]`. Handle counter regressions (`procDelta < 0` or `totalDelta <= 0`) safely by clamping to `0.0%`. |
| **FR-06** | **P0** | **Configurable `USER_HZ` (`CLK_TCK`):** Default `USER_HZ` to `100` ticks/second (`_SC_CLK_TCK` standard on x86_64/ARM64 Linux) and expose it via `EngineConfig.UserHZ` and `--user-hz` CLI flag so non-standard kernels and deterministic unit tests can override it without requiring CGO. |
| **FR-07** | **P0** | **Fault Tolerance for Ephemeral & Protected Processes:** Gracefully skip processes that vanish mid-scan (`fs.ErrNotExist` / `ESRCH`), return permission denied (`fs.ErrPermission` / `EACCES` on `/proc/[pid]/smaps_rollup` or restricted `/proc` mounts), or contain truncated/malformed lines. Increment diagnostic counters (`SkippedVanished`, `SkippedPermission`, `ParseErrors`) in `SystemView` without crashing or aborting the tick. |
| **FR-08** | **P0** | **Bounded Historical Ring Buffers:** Store the last `HistoryCapacity` ticks (default `300`) of system metrics (`CPU%`, `Mem%`, `TotalProcs`, `ZombieCount`) and active per-process CPU/RSS samples in generic fixed-size circular ring buffers (`RingBuffer[T]`). Prune per-process buffers immediately when a `ProcessKey` is absent in the latest tick. |
| **FR-09** | **P0** | **Configurable Alerting Engine with Hysteresis:** Support threshold rules for: (1) System CPU% > threshold, (2) System Memory% > threshold, (3) Single Process CPU% > threshold, (4) Single Process RSS MB > threshold, and (5) Total Zombie (`state == 'Z'`) count > threshold. An alert transitions to `FIRING` only when the condition holds for `>= SustainedTicks` consecutive ticks (default `3`), and transitions to `RESOLVED` when the metric drops below the threshold. |
| **FR-10** | **P0** | **Interactive Bubble Tea TUI:** Render: (1) Top system gauge bar (CPU%, Mem Used/Total/%, Swap, Load/Procs/Zombies, Mode indicator `NORMALIZED`/`IRIX`, `USER_HZ`), (2) ASCII sparkline of historical system CPU% and Mem%, (3) Interactive process table showing `PID`, `USER/UID`, `STATE`, `CPU%`, `MEM%`, `RSS`, `VMS`, `PSS` (shown as `N/A` when disabled/unreadable), `THREADS`, and `COMMAND`, (4) Live Alert banner/drawer showing recent `FIRING`/`RESOLVED` alerts. Support keybindings: `c` (sort CPU%), `m` (sort RSS/Mem%), `p` (sort PID), `v` (sort VMS), `i` (toggle Irix/Normalized CPU%), `/` (substring filter on command/PID), `↑/↓/j/k` (scroll), `q`/`Ctrl+C` (clean exit). |
| **FR-11** | **P1** | **Headless CLI & JSONL Alert Export:** Support `--headless --ticks=N` (or `--once` for a 2-sample, 1-delta single report printed as JSON/table to `stdout`) for scriptable verification, and `--alert-log=/path/to/alerts.jsonl` to append structured `log/slog` JSON records on every alert state transition. |
| **FR-12** | **P1** | **Future Prometheus Integration Boundary:** Define the `MetricsSink` interface (`OnSnapshot(ctx context.Context, view model.SystemView) error`) in `internal/model/sink.go` and register the Alert Evaluator as a `MetricsSink`. Document the exact metric naming, label cardinality constraints, and Phase 2 implementation blueprint for `PromExporter` in code comments and `README.md`. |

---

## 5. Non-Functional, Security & Environment Constraints

### Target Environment & Compatibility

* **Primary Target OS:** Pop!_OS 24.04 LTS (Linux kernel 6.8+, COSMIC Wayland session, `zsh` shell) on x86_64 (`AMD Ryzen 7 7700`) and ARM64 Linux.
* **Privilege Model:** Must run cleanly as an unprivileged standard user (`uid >= 1000`). Any `/proc/[pid]/*` file unreadable due to `hidepid=2` or root ownership (`smaps_rollup`) must degrade gracefully without terminating the scan or polluting `stderr` during TUI rendering.

### Performance Targets & Honest Reporting Mandate

* **Measurable Targets (Not Hardcoded Guarantees):**
* **Target Self-CPU Overhead:** `< 1.0%` of a single logical CPU core during steady-state 1Hz polling on a typical desktop workload (200–400 processes) with `--enable-pss=false`.
* **Target Scan Latency:** `< 5.0 ms` per full `/proc` collection pass against the deterministic 300-process benchmark fixture (`BenchmarkCollector_300Procs`) on modern x86_64 hardware.
* **Allocation Discipline:** Avoid `fmt.Sscanf` and regular expressions in hot `/proc` loops; reuse read buffers or use lightweight byte/slice scanning (`strconv.ParseUint`, `bytes.IndexByte`).


* **Honest Reporting Rule:** Hardware clock speeds, kernel configurations, and `/proc` contention vary. The verification suite and `README.md` must report **actual measured `go test -bench` latency, bytes/op, allocs/op, and live self-CPU observations** produced on the host machine—never fabricated or hardcoded numbers. If a target threshold is exceeded on a specific workload (e.g., when `--enable-pss` walks kernel page tables), the exact measured trade-off must be documented transparently.

### Concurrency & Memory Safety

* **Zero Data Races:** All concurrent state transitions between the collector loop, engine, alert sink, and Bubble Tea TUI loop must pass `go test -race ./...` with zero warnings.
* **Bounded Memory Footprint:** All historical buffers (`RingBuffer[T]`) and alert logs in memory must use fixed-capacity circular slices pre-allocated at initialization. Memory usage must remain flat ($O(P)$ where $P$ is active process count) over multi-hour runs.

### Security & Input Sanitization

* **Terminal Escape Injection Prevention:** Process command lines (`/proc/[pid]/cmdline`) and `comm` strings can contain arbitrary control characters or malicious ANSI escape sequences planted by local processes. Before passing `Comm` or `Cmdline` to the TUI or logs, strip or replace ASCII control characters (`< 0x20` and `0x7f`) with spaces.
* **No Network Listeners in v1.0:** The MVP opens zero network sockets. File writes occur only if the user explicitly passes `--alert-log`.

---

## 6. Data Models, Interfaces & Directory Layout

### Proposed Directory Structure

```text
pulse-linux-observatory/
├── cmd/
│   └── pulse/
│       └── main.go                 # Entrypoint, CLI flags, config loader, mode router (TUI vs Headless)
├── internal/
│   ├── model/
│   │   ├── metrics.go              # ProcessKey, ProcessMetrics, SystemSnapshot, SystemView
│   │   ├── ring.go                 # Generic fixed-capacity RingBuffer[T]
│   │   ├── config.go               # EngineConfig, AlertConfig, JSON config loader
│   │   └── sink.go                 # MetricsSink interface (Alert & Future Prometheus boundary)
│   ├── procfs/
│   │   ├── reader.go               # fs.FS Collector implementation & fault-tolerant PID scanning
│   │   ├── parser.go               # Low-allocation parsers for stat, meminfo, pid/stat, status, smaps_rollup
│   │   └── parser_test.go          # Deterministic unit tests, edge-case tests & 300-proc benchmarks
│   ├── engine/
│   │   ├── engine.go               # Two-sample delta calculation, PID reuse detection, ring buffer updates
│   │   └── engine_test.go          # Multi-tick delta math, PID reuse, counter wrap & race tests
│   ├── alert/
│   │   ├── evaluator.go            # Hysteresis rule engine implementing model.MetricsSink + slog JSONL writer
│   │   └── evaluator_test.go       # Sustained tick firing, recovery, and non-flapping unit tests
│   └── tui/
│       ├── app.go                  # Bubble Tea Model, Update, View, sorting, filtering & sparklines
│       ├── styles.go               # Lip Gloss styles and layout helpers
│       └── app_test.go             # Headless TUI state machine & keybinding tests
├── testdata/
│   └── proc_fixtures/
│       ├── tick_01/                # Baseline synthetic /proc tree at T=0s
│       ├── tick_02/                # T=1s synthetic /proc tree (CPU spike, memory growth, PID reuse case)
│       └── malformed/              # Corrupted stat lines, weird comm names "(a (b) c)", missing files
├── go.mod
├── go.sum
├── PROJECT.md
└── README.md

```

### Core Data Models (`internal/model/metrics.go`)

```go
package model

import "time"

// ProcessKey uniquely identifies a Linux process across ticks, preventing
// misattribution when the kernel recycles PIDs rapidly.
type ProcessKey struct {
    PID       int    `json:"pid"`
    StartTime uint64 `json:"start_time"` // Field 22 of /proc/[pid]/stat (clock ticks since boot)
}

// RawProcessSample holds raw counters parsed directly from /proc/[pid]/* at a single instant.
type RawProcessSample struct {
    Key          ProcessKey
    PPID         int
    UID          uint32
    Comm         string // Sanitized executable name from /proc/[pid]/stat
    Cmdline      string // Sanitized full command line (falls back to [Comm])
    State        byte   // 'R', 'S', 'D', 'Z', 'T', 'I', etc.
    UTimeJiffies uint64 // Field 14: user mode jiffies
    STimeJiffies uint64 // Field 15: kernel mode jiffies
    NumThreads   int    // Field 20
    VMSBytes     uint64 // Virtual Memory Size (bytes)
    RSSBytes     uint64 // Resident Set Size (bytes, includes shared pages)
    PSSBytes     uint64 // Proportional Set Size (bytes, 0 if disabled/unreadable)
    PSSValid     bool   // True only if /proc/[pid]/smaps_rollup was read and parsed
}

// RawSystemSample holds raw counters from /proc/stat and /proc/meminfo at a single instant.
type RawSystemSample struct {
    Timestamp         time.Time
    TotalJiffies      uint64 // Sum of first eight CPU fields (guest excluded) on the aggregate 'cpu ' line
    ActiveJiffies     uint64 // TotalJiffies minus (idle + iowait)
    NumCores          int    // Count of cpu0..cpuN lines in /proc/stat
    MemTotalBytes     uint64
    MemAvailableBytes uint64
    MemUsedBytes      uint64 // MemTotalBytes - MemAvailableBytes
    SwapTotalBytes    uint64
    SwapUsedBytes     uint64
    Processes         []RawProcessSample
    SkippedVanished   int // Processes that disappeared during directory walk
    SkippedPermission int // Processes/files skipped due to EACCES/EPERM
    ParseErrors       int // Malformed /proc files encountered and safely skipped
}

// ProcessMetrics holds computed delta telemetry for a single process on a given tick.
type ProcessMetrics struct {
    Key            ProcessKey `json:"key"`
    PPID           int        `json:"ppid"`
    UID            uint32     `json:"uid"`
    Comm           string     `json:"comm"`
    Cmdline        string     `json:"cmdline"`
    State          string     `json:"state"`
    CPUPercentNorm float64    `json:"cpu_percent_norm"` // 0.0 to 100.0 (share of total system capacity)
    CPUPercentIrix float64    `json:"cpu_percent_irix"` // 0.0 to 100.0 * NumCores (100% = 1 full core)
    MemPercent     float64    `json:"mem_percent"`      // 100.0 * RSSBytes / MemTotalBytes
    VMSBytes       uint64     `json:"vms_bytes"`
    RSSBytes       uint64     `json:"rss_bytes"`
    PSSBytes       uint64     `json:"pss_bytes,omitempty"`
    PSSValid       bool       `json:"pss_valid"`
    NumThreads     int        `json:"num_threads"`
}

// SystemHistoryPoint is stored in the system-level RingBuffer for sparklines & trend analysis.
type SystemHistoryPoint struct {
    Timestamp   time.Time `json:"timestamp"`
    CPUPercent  float64   `json:"cpu_percent"`
    MemPercent  float64   `json:"mem_percent"`
    TotalProcs  int       `json:"total_procs"`
    ZombieCount int       `json:"zombie_count"`
}

// SystemView is the immutable snapshot broadcast by the Engine on each tick.
type SystemView struct {
    TickNumber        uint64               `json:"tick_number"`
    Timestamp         time.Time            `json:"timestamp"`
    SampleInterval    time.Duration        `json:"sample_interval"`
    NumCores          int                  `json:"num_cores"`
    UserHZ            uint64               `json:"user_hz"`
    CPUPercent        float64              `json:"cpu_percent"`
    MemTotalBytes     uint64               `json:"mem_total_bytes"`
    MemUsedBytes      uint64               `json:"mem_used_bytes"`
    MemAvailableBytes uint64               `json:"mem_available_bytes"`
    MemPercent        float64              `json:"mem_percent"`
    SwapTotalBytes    uint64               `json:"swap_total_bytes"`
    SwapUsedBytes     uint64               `json:"swap_used_bytes"`
    TotalProcs        int                  `json:"total_procs"`
    RunningProcs      int                  `json:"running_procs"`
    ZombieCount       int                  `json:"zombie_count"`
    SkippedVanished   int                  `json:"skipped_vanished"`
    SkippedPermission int                  `json:"skipped_permission"`
    ParseErrors       int                  `json:"parse_errors"`
    ScanDuration      time.Duration        `json:"scan_duration"`
    Processes         []ProcessMetrics     `json:"processes"`
    History           []SystemHistoryPoint `json:"history"`
    ActiveAlerts      []AlertEvent         `json:"active_alerts"`
}

```

### Core Interfaces & Future Prometheus Contract (`internal/model/sink.go`)

```go
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

```

### Future Prometheus Integration Design (Phase 2 Blueprint — Not Implemented in v1.0)

To satisfy Requirement 5 without introducing unused dependencies or background HTTP servers in v1.0, `PROJECT.md` and `README.md` define the exact Phase 2 Prometheus integration architecture:

1. **Attachment Point:** A future `internal/prom` package will implement `model.MetricsSink` and register with `engine.RegisterSink(promSink)` when `--prometheus-addr=:9100` is passed.
2. **Exported Metric Families:**
* `pulse_system_cpu_utilization_ratio` (Gauge, `0.0–1.0`)
* `pulse_system_memory_bytes{state="total|used|available|swap_used"}` (Gauge)
* `pulse_processes_by_state{state="R|S|D|Z|T"}` (Gauge)
* `pulse_collector_scan_duration_seconds` (Histogram / Gauge)
* `pulse_collector_skipped_total{reason="vanished|permission|parse_error"}` (Counter)
* `pulse_top_process_cpu_ratio{comm="...",rank="1..10"}` and `pulse_top_process_memory_bytes{comm="...",type="rss|vms|pss",rank="1..10"}` (Gauge)


3. **High-Cardinality Guardrail:** Ephemeral Linux `pid` and `starttime` values must **never** be exported as unbounded Prometheus labels, as process churn would cause TSDB time-series explosion. Phase 2 will export per-process metrics strictly aggregated by `comm` or bounded to the top-$K$ processes (`rank="1..K"`).

### CLI Flags & Configuration Contract (`cmd/pulse/main.go`)

* `--proc-root string` (default `"/proc"`): Root path of the proc filesystem (allows pointing at fixture directories).
* `--interval duration` (default `1s`): Polling interval between `/proc` scans.
* `--history-size int` (default `300`): Capacity of in-memory circular ring buffers (ticks).
* `--user-hz uint` (default `100`): Kernel `USER_HZ` (`_SC_CLK_TCK`) value used for Irix CPU% math.
* `--irix` (default `false`): Start with Irix CPU% mode (`100% = 1 core`) instead of Normalized mode (`100% = all cores`).
* `--enable-pss` (default `false`): Attempt to read `/proc/[pid]/smaps_rollup` to populate `PSSBytes` alongside `RSSBytes` and `VMSBytes`.
* `--cpu-warn float` (default `85.0`): System/Process CPU% alert threshold.
* `--mem-warn-mb uint` (default `2048`): Single-process RSS alert threshold in MiB (`2048` MiB = 2 GiB).
* `--sys-mem-warn float` (default `90.0`): System Memory% alert threshold.
* `--zombie-warn int` (default `5`): Zombie process count alert threshold.
* `--sustained-ticks int` (default `3`): Consecutive ticks a threshold must be breached before firing an alert.
* `--config string` (default `""`): Optional path to JSON configuration file overriding alert/engine defaults.
* `--alert-log string` (default `""`): Optional file path to append structured JSONL alert events.
* `--once` (default `false`): Perform two samples separated by `--interval` (default overridden to `200ms` when `--once` is set and `--interval` is not explicitly provided), print a single JSON or formatted summary to `stdout`, and exit `0`.
* `--headless` (default `false`): Run without TUI for `--ticks N` iterations, outputting structured summaries.
* `--ticks int` (default `0`): Number of ticks to run in `--headless` mode before exiting (`0` = run until signal).

---

## 7. Verification, Testing Strategy & Acceptance Criteria

### Deterministic `/proc` Fixture Specification (`testdata/proc_fixtures/`)

All unit tests must run hermetically without reading the host's `/proc`:

1. **`testdata/proc_fixtures/tick_01/` (Baseline $T = 0\text{s}$):**
* `stat`: `cpu  10000 0 2000 88000 0 0 0 0 0 0` (Total = `100,000`, Active = `12,000`), `cpu0`, `cpu1`, `cpu2`, `cpu3` (4 cores).
* `meminfo`: `MemTotal: 16384000 kB`, `MemFree: 4096000 kB`, `MemAvailable: 8192000 kB`, `Buffers: 512000 kB`, `Cached: 3584000 kB`, `SwapTotal: 4096000 kB`, `SwapFree: 4096000 kB`.
* `101/` (Normal process `nginx`): `stat` with `comm=(nginx)`, `state=S`, `utime=100`, `stime=50`, `starttime=5000`, `vsize=104857600` (100 MiB), `rss=5120` (20 MiB at 4 KiB page size). `status` with `Uid: 1000 1000 1000 1000`. `cmdline` with `nginx: master process\x00-g\x00daemon off;\x00`. `smaps_rollup` with `Pss: 12288 kB` (12 MiB).
* `202/` (Process with spaces/parens in comm): `stat` with `comm=(Web Content (1))`, `state=R`, `utime=500`, `stime=100`, `starttime=6000`, `vsize=2147483648` (2 GiB), `rss=262144` (1 GiB).
* `303/` (Process about to be killed & PID-reused in `tick_02`): `stat` with `comm=(old-worker)`, `state=S`, `utime=9000`, `stime=1000`, `starttime=7000`, `vsize=10485760`, `rss=1024`.


2. **`testdata/proc_fixtures/tick_02/` ($T = 1\text{s}$, $\Delta t = 1.0\text{s}$, $\Delta\text{TotalJiffies} = 400$ across 4 cores at `USER_HZ=100`):**
* `stat`: `cpu  10150 0 2050 88200 0 0 0 0 0 0` (Total = `100,400`, $\Delta\text{Total} = 400$; Active = `12,200`, $\Delta\text{Active} = 200 \implies \text{System CPU} = 50.0\%$).
* `101/` (`nginx`): `utime=120`, `stime=50` ($\Delta\text{proc} = 20$ jiffies $\implies \text{Normalized CPU} = 20/400 = 5.0\%$, $\text{Irix CPU} = 20/(1.0 \times 100) = 20.0\%$).
* `202/` (`Web Content (1)`): `utime=650`, `stime=130` ($\Delta\text{proc} = 180$ jiffies $\implies \text{Normalized CPU} = 180/400 = 45.0\%$, $\text{Irix CPU} = 180.0\%$), `rss=655360` (2.5 GiB).
* `303/` (PID Reuse Case — `new-worker` recycled PID `303`!): `stat` with `comm=(new-worker)`, `state=R`, `utime=10`, `stime=5`, **`starttime=7095`** (different from `7000`!). Must be detected as a new `ProcessKey{PID: 303, StartTime: 7095}` with initial `CPUPercentNorm = 0.0%` (not negative or corrupted by `old-worker`'s `10,000` jiffies).
* `404/` (Zombie process): `stat` with `comm=(defunct-child)`, `state=Z`, `utime=0`, `stime=0`, `starttime=7090`, `vsize=0`, `rss=0`.


3. **`testdata/proc_fixtures/malformed/`:**
* Contains truncated `/proc/[pid]/stat` files, non-numeric `/proc` entries (`self`, `thread-self`, `sys`), unreadable permission tests via custom `fs.FS` wrapper returning `fs.ErrPermission` and `fs.ErrNotExist`, and control-character-injected `cmdline` strings (`"bad\x1b[31mcmd\x00"`).



### Exact Verification Commands

```zsh
# 1. Unit tests and deterministic fixture suite
CGO_ENABLED=0 go test -v ./...

# 2. Concurrency race detector suite
go test -race ./...

# 3. Static analysis
go vet ./...

# 4. Performance & allocation benchmarks (300-process synthetic fixture)
CGO_ENABLED=0 go test -bench=. -benchmem ./internal/procfs/... ./internal/engine/...

# 5. Build static binary and run single-pass verification against fixtures and live /proc
CGO_ENABLED=0 go build -o bin/pulse ./cmd/pulse
./bin/pulse --proc-root=./testdata/proc_fixtures/tick_01 --once
./bin/pulse --once

```

### Measurable Acceptance Criteria

1. **Zero C-Dependencies:** `ldd bin/pulse` reports `not a dynamic executable` (or pure static Go binary when built with `CGO_ENABLED=0`).
2. **100% Deterministic Math Accuracy:** Unit tests against `tick_01` $\rightarrow$ `tick_02` assert exact floating-point equality (within `1e-4`) for System CPU (`50.0%`), `PID 101` Normalized (`5.0%`) & Irix (`20.0%`), `PID 202` Normalized (`45.0%`) & Irix (`180.0%`), and `PID 303` PID-reuse reset (`0.0%`).
3. **Fault Resilience:** Injecting `fs.ErrNotExist`, `fs.ErrPermission`, and malformed `/proc/[pid]/stat` entries causes zero panics and increments `SkippedVanished`, `SkippedPermission`, and `ParseErrors` accurately.
4. **Hysteresis Verification:** Alert tests prove that a breach lasting `SustainedTicks - 1` ticks emits **zero** alerts, a breach reaching `SustainedTicks` ticks emits exactly **one** `FIRING` transition event, subsequent ticks above threshold do not re-emit duplicate `FIRING` events, and dropping below threshold emits **one** `RESOLVED` event.
5. **Reproducible Benchmarks & Honest Reporting:** `BenchmarkCollector_300Procs` executes cleanly and records actual scan latency (target `< 5ms`) and heap allocations in `README.md`.

---

## 8. Implementation Milestones (Sequential Vertical Slices)

### Milestone 1 (M1): Domain Models, Deterministic `/proc` Collector & Fixture Suite

* **Objective:** Establish the Go module, domain types (`ProcessKey`, `RawSystemSample`, `RingBuffer[T]`), `io/fs.FS` `/proc` parser (`stat`, `meminfo`, `[pid]/stat`, `[pid]/status`, `[pid]/cmdline`, optional `[pid]/smaps_rollup`), sanitization helpers, and synthetic test fixtures.
* **Implementation Scope:**
* Initialize `go.mod` (`github.com/khalid7344/pulse-linux-observatory` or local module path `pulse-linux-observatory`).
* Implement `internal/model/metrics.go`, `internal/model/ring.go`, `internal/model/config.go`, and `internal/model/sink.go`.
* Implement `internal/procfs/parser.go` and `internal/procfs/reader.go` with reverse-`)` scanning for `comm`, control-character sanitization, optional `smaps_rollup` `Pss:` parsing, and explicit classification of `fs.ErrNotExist`, `fs.ErrPermission`, and parse errors.
* Populate `testdata/proc_fixtures/tick_01`, `tick_02`, and `malformed`, plus a programmatic 300-process `testing/fstest.MapFS` benchmark generator in `internal/procfs/parser_test.go`.


* **Expected Deliverables:** `go.mod`, `internal/model/*`, `internal/procfs/*`, `testdata/proc_fixtures/*`.
* **Verification Steps:**
* Run `CGO_ENABLED=0 go test -v ./internal/model/... ./internal/procfs/...`
* Run `CGO_ENABLED=0 go test -bench=. -benchmem ./internal/procfs/...`


* **Definition of Done:** All `/proc` parsers and `RingBuffer[T]` unit tests pass 100%, edge cases (`(Web Content (1))`, missing files, permission errors, ANSI escape sanitization) are verified, and `go vet ./...` reports zero issues.

### Milestone 2 (M2): Telemetry Delta Engine, PID Reuse Handling & Exporter Boundary

* **Objective:** Build the stateful two-sample telemetry engine that computes system and per-process CPU/memory metrics, detects PID reuse via `ProcessKey{PID, StartTime}`, manages bounded historical ring buffers, and dispatches `SystemView` snapshots to registered `MetricsSink` consumers.
* **Implementation Scope:**
* Implement `internal/engine/engine.go` with configurable `UserHZ` (default `100`), Normalized and Irix CPU% calculation, counter-regression clamping, dead-PID pruning, and `MetricsSink` fan-out.
* Write comprehensive multi-tick state transition tests in `internal/engine/engine_test.go` covering: `tick_01` $\rightarrow$ `tick_02` delta accuracy, PID `303` reuse (`StartTime` `7000` $\rightarrow$ `7095`), counter wrap-around clamping, and concurrent `MetricsSink` dispatch under `-race`.


* **Expected Deliverables:** `internal/engine/engine.go`, `internal/engine/engine_test.go`.
* **Verification Steps:**
* Run `go test -v -race ./internal/engine/...`
* Run `CGO_ENABLED=0 go test -bench=. -benchmem ./internal/engine/...`


* **Definition of Done:** Multi-tick CPU/Memory delta math, `ProcessKey` PID-reuse reset, and ring-buffer retention pass all unit and `-race` tests.

### Milestone 3 (M3): Configurable Hysteresis Alerting Engine & Structured Audit Logging

* **Objective:** Implement the threshold alerting engine with sustained-tick hysteresis (`SustainedTicks`) as a `model.MetricsSink`, maintaining an in-memory alert history ring buffer and optional `log/slog` JSONL file output.
* **Implementation Scope:**
* Implement `internal/alert/evaluator.go` supporting System CPU%, System Mem%, Process CPU%, Process RSS MB, and Zombie count rules.
* Track per-rule/per-`ProcessKey` consecutive breach counters so transient spikes shorter than `SustainedTicks` never fire, and dead/reused PIDs clear their pending breach state cleanly.
* Implement `internal/alert/evaluator_test.go` verifying hysteresis, deduplication of active alerts, resolution transitions, and JSONL file output.


* **Expected Deliverables:** `internal/alert/evaluator.go`, `internal/alert/evaluator_test.go`.
* **Verification Steps:**
* Run `go test -v -race ./internal/alert/...`


* **Definition of Done:** All alert rules, hysteresis counters, resolution transitions, and JSONL audit log tests pass with zero data races.

### Milestone 4 (M4): Interactive Bubble Tea TUI & Headless CLI Entrypoint

* **Objective:** Build the interactive terminal UI (`internal/tui`) and wire the complete application entrypoint (`cmd/pulse/main.go`) supporting both live TUI operation and `--once` / `--headless` CLI modes.
* **Implementation Scope:**
* Add `github.com/charmbracelet/bubbletea` and `github.com/charmbracelet/lipgloss` to `go.mod`.
* Implement `internal/tui/app.go` and `internal/tui/styles.go` with system header gauges, ASCII historical sparklines, process table sorting (`c`, `m`, `p`, `v`), mode toggle (`i` for Irix/Normalized), live substring filtering (`/`), `RSS`/`VMS`/`PSS` display columns, and the active alert drawer.
* Implement headless state/keybinding unit tests in `internal/tui/app_test.go`.
* Implement `cmd/pulse/main.go` with all specified CLI flags, optional JSON config loading (`--config`), graceful `SIGINT`/`SIGTERM` shutdown, and `--once` / `--headless` output modes.


* **Expected Deliverables:** `internal/tui/*`, `cmd/pulse/main.go`, compiled binary `bin/pulse`.
* **Verification Steps:**
* Run `go test -v -race ./...`
* Run `./bin/pulse --proc-root=./testdata/proc_fixtures/tick_01 --once`
* Run `./bin/pulse --once --enable-pss` against live `/proc`.


* **Definition of Done:** Binary compiles cleanly with `CGO_ENABLED=0`, TUI state/keybinding unit tests pass, and `--once` / `--headless` modes execute accurately against both synthetic fixtures and the live Pop!_OS `/proc` filesystem.

### Milestone 5 (M5): End-to-End Verification, Performance Benchmarking & Portfolio `README.md`

* **Objective:** Execute full end-to-end verification, record honest reproducible performance benchmarks and live self-CPU overhead measurements on Pop!_OS, and author a comprehensive, production-grade `README.md`.
* **Implementation Scope:**
* Run the full benchmark suite (`BenchmarkCollector_300Procs`, `BenchmarkEngine_Tick`) and measure live self-CPU% of `pulse` running at 1Hz on the host system.
* Verify all CLI flags, `--config` JSON loading, and `--alert-log` JSONL emission in an end-to-end integration test (`cmd/pulse/main_test.go`).
* Author `README.md` covering:
1. System architecture diagram and package design.
2. Mathematical formulation of two-sample `/proc` CPU deltas (Normalized vs. Irix) and the `100 Hz` `USER_HZ` (`_SC_CLK_TCK`) design decision.
3. PID reuse prevention via composite `ProcessKey{PID, StartTime}`.
4. Memory accounting deep dive (`VMS` vs. `RSS` shared-page overcounting vs. `smaps_rollup` `PSS` and kernel page-walk overhead).
5. Reproducible benchmark results table (actual measured scan latency, allocs/op, bytes/op, and live self-CPU% overhead).
6. Usage guide, CLI flags, JSON configuration example, and keybindings.
7. Phase 2 Prometheus Exporter blueprint (`MetricsSink` implementation and cardinality guardrails).




* **Expected Deliverables:** `cmd/pulse/main_test.go`, `README.md`.
* **Verification Steps:**
* Run `go test -v -race ./...`
* Run `CGO_ENABLED=0 go test -bench=. -benchmem ./...`
* Run `go vet ./...`


* **Definition of Done:** All tests and race checks pass 100%, actual benchmark and self-CPU figures are recorded honestly in `README.md`, and the repository is clean and ready for release.

---

## 9. Assumptions & Open Questions

### Accepted Defaults & Confirmed Decisions

1. **Historical Storage:** Bounded in-memory circular ring buffers (`--history-size=300` ticks by default) plus optional append-only JSONL alert logging (`--alert-log`). No external or embedded disk database.
2. **Alert Configuration:** Built-in CLI flags (`--cpu-warn`, `--mem-warn-mb`, `--sys-mem-warn`, `--zombie-warn`, `--sustained-ticks`) plus optional flat JSON configuration file (`--config`) parsed via standard library `encoding/json`.
3. **CPU% Normalization Mode:** Defaults to **Normalized mode** (`0–100%` of total logical CPU capacity) with runtime toggle (`i` key) and CLI flag (`--irix`) for **Irix mode** (`0–100% * NumCores`).
4. **Kernel Clock Tick (`USER_HZ`):** Defaults to `100` Hz (standard Linux `_SC_CLK_TCK`) to maintain `CGO_ENABLED=0` portability, configurable via `EngineConfig.UserHZ` and `--user-hz`.
5. **External Dependencies:** Strictly restricted to `github.com/charmbracelet/bubbletea` and `github.com/charmbracelet/lipgloss` beyond the Go standard library.
6. **Memory Accounting (`RSS`, `VMS`, `PSS`):** `RSS` and `VMS` are always collected from `/proc/[pid]/stat` (with page size defaulted to `os.Getpagesize()`, overridable in parser config for deterministic tests). `PSS` is collected optionally from `/proc/[pid]/smaps_rollup` when `--enable-pss` is passed, gracefully degrading when unreadable.
7. **Performance Targets:** `< 1%` self-CPU overhead and `< 5ms` scan latency are treated as measurable engineering targets backed by reproducible benchmarks and transparent reporting, not fabricated guarantees.

### Unresolved Questions

* **None.** All architectural, mathematical, and scope boundaries are locked for M1–M5 execution.

---

## 10. Current execution state

Implementation exists for M1–M5. The current engineering audit, test evidence and
remaining limitations are documented in AUDIT.md and PROGRESS.md. GitHub publication,
remote CI and minimum-Go runtime verification remain pending.
The implementation requirements above are design targets, not certification that
every acceptance criterion has been met. In particular, no current steady-state
CPU overhead or multihour resource-retention measurement is available.

The current dependency minimum is Go 1.24.0. No Prometheus service is implemented.
Next task: restore GitHub access, verify repository availability, then publish under the owner’s authorization.
