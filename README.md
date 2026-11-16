# Pulse Linux Observatory (`pulse`)

[![Go Version](https://img.shields.io/badge/Go-1.24%2B-00ADD8?logo=go)](https://golang.org)
[![CI](https://github.com/khalid7344/pulse-linux-observatory/actions/workflows/ci.yml/badge.svg)](https://github.com/khalid7344/pulse-linux-observatory/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

**Pulse Linux Observatory** is a Linux telemetry engine and interactive Terminal UI (TUI) engineered for systems programmers, Linux platform engineers, and performance-conscious operators. Built on modern Go with `CGO_ENABLED=0`, Pulse directly parses Linux `/proc` pseudo-files through Go's `io/fs.FS` abstraction, protecting cross-sample CPU accounting against PID reuse, distinguishing virtual vs. physical vs. proportional memory pressure, enforcing hysteresis-backed threshold alerting, and retaining bounded historical circular buffers.

![Pulse TUI displaying the checked-in synthetic proc fixture](docs/screenshot.png)

*Actual TUI capture using synthetic fixture data. Fixed counters yield zero CPU deltas.*

---

## 1. Architecture & Package Design

Pulse adopts a clean hexagonal domain architecture separated into distinct layers coordinated via `context.Context`:

```text
cmd/pulse: flags, config, signals and execution modes
    |
    v
procfs: read /proc through an injected fs.FS
    |
    v
engine: two-sample CPU deltas, PID identity and bounded history
    |
    +--> alert: sustained thresholds and optional JSONL logging
    |
    +--> tui / headless reports: snapshots and alert state
```

`MetricsSink` is the extension boundary for downstream consumers. The current
alert evaluator implements it; Prometheus integration is a future design, not a
running exporter.

### Core Packages

* **`internal/model`**: Domain data structures (`ProcessKey`, `RawProcessSample`, `ProcessMetrics`, `SystemView`, `AlertEvent`), generic circular `RingBuffer[T]`, and the extensible `MetricsSink` interface.
* **`internal/procfs`**: Resilient `/proc` parsers operating over `io/fs.FS`. Uses reverse-parenthesis scanning for arbitrary process `comm` strings, ASCII control-character sanitization, buffered reads on 0-byte pseudo-files, and graceful fault-tolerance against `fs.ErrNotExist` and `fs.ErrPermission`.
* **`internal/engine`**: Stateful two-sample telemetry calculator. Maintains composite `ProcessKey` identity, computes Normalized vs. Irix CPU% deltas, prunes dead PID ring buffers to maintain an `O(P)` memory bound, where `P` is the visible process count, and fans out immutable `SystemView` snapshots.
* **`internal/alert`**: Hysteresis-backed alerting engine implementing `model.MetricsSink`. Tracks consecutive breach counters across ticks (`SustainedTicks`) to eliminate transient spike flapping, and streams structured JSONL events to disk via `log/slog`.
* **`internal/tui`**: Responsive Elm-architecture terminal UI powered by Bubble Tea and Lip Gloss. Features ASCII gauges, Unicode sparklines, live substring filtering (`/`), sorting (`c`, `m`, `p`, `v`), and expandable alert notifications.
* **`cmd/pulse`**: Single entrypoint handling CLI flags, JSON configuration loading, headless batch execution (`--headless`, `--once`), and OS signal termination.

---

## 2. Two-sample CPU deltas

Linux exposes cumulative CPU counters in clock ticks, not instantaneous CPU usage.
Pulse compares a baseline sample with the next sample. `delta(x)` means the current
counter minus its previous value, clamped to zero if the counter regresses.

### System CPU utilization

The first eight aggregate fields in `/proc/stat` give:

```text
total  = user + nice + system + idle + iowait + irq + softirq + steal
active = total - idle - iowait
system_cpu_percent = 100 * delta(active) / delta(total)
```

When `delta(total)` is zero, system CPU usage is zero. Results are clamped to
`0..100%`. `guest` and `guest_nice` are already included in `user` and `nice`;
adding them again would double-count guest time.

### Per-process utilization

A process's counter is `utime + stime` from `/proc/[pid]/stat`. Its two samples must
have the same `PID` and `starttime`; a new or recycled process starts at zero.

```text
process_ticks = utime + stime
normalized_percent = 100 * delta(process_ticks) / delta(total)
irix_percent = 100 * delta(process_ticks) / (elapsed_seconds * USER_HZ)
```

Normalized mode measures a share of the whole machine and is clamped to `0..100%`.
A zero total delta yields zero normalized usage. In Irix mode, `100%` means one
logical core; usage is clamped to `0..(100 * num_cores)%`. The CLI `--irix` flag and
TUI `i` key select the displayed mode. Process CPU alerts use normalized usage.

### Clock frequency

`USER_HZ` defaults to 100 ticks per second. Check your host with `getconf CLK_TCK`
and use `--user-hz` if needed. Keeping it configurable avoids a CGO dependency.
The denominator uses elapsed sampling time in seconds, not a duration in nanoseconds.

---

## 3. PID Reuse Safety via Composite Identity

Naive process monitors that index processes solely by numeric `PID` suffer from a critical flaw: **PID recycling**. When a short-lived process dies and a new process is spawned with the same PID between sample ticks, subtracting counters from the dead process results in false negative deltas or massive phantom CPU spikes.

Pulse eliminates this defect through composite identity:

```go
type ProcessKey struct {
    PID       int    // Linux Process ID
    StartTime uint64 // Field 22 of /proc/[pid]/stat (clock ticks since boot)
}
```

* **Behavior on Recycling:** When PID `303` dies and a new process reuses PID `303`, the kernel assigns a new `StartTime` (e.g., `7000` to `7095`).
* **Engine Response:** The engine detects that `ProcessKey{303, 7095}` does not match `ProcessKey{303, 7000}`. Rather than computing an invalid delta, it treats the recycled PID as a brand-new process:
  * Initializes delta CPU% to `0.0%` on its first tick.
  * Resets per-process historical ring buffers.
  * Cleans up pending alert breach counters from the prior process.

---

## 4. Memory Accounting: `VMS` vs. `RSS` vs. `PSS`

Memory consumption on modern virtual-memory Linux systems is frequently misunderstood:

| Metric | Origin | Description | Trade-offs & Limitations |
| :--- | :--- | :--- | :--- |
| **VMS** (Virtual Memory Size) | `/proc/[pid]/stat` (field 23 `vsize`) | Total virtual address space allocated, including mapped files, shared libraries, heap, and thread stacks. | Misleadingly large. A process mapping a 10 GB file uses 10 GB VMS even if only 4 KB resides in RAM. |
| **RSS** (Resident Set Size) | `/proc/[pid]/stat` (field 24 `rss * page_size`) | Physical memory pages currently resident in RAM. | **Overcounts shared memory**. Shared libraries (`libc.so`) and shared memory segments (`shm`) are counted in *every* process mapping them. |
| **PSS** (Proportional Set Size) | `/proc/[pid]/smaps_rollup` (`Pss:`) | Physical memory uniquely held plus shared pages divided proportionally by the number of mapping processes. | Attributes shared resident pages proportionally; it is not a measure of exclusive allocation. Requires kernel page-table traversal, introducing higher scan overhead. |

### Optional PSS Scan Overhead Guardrail
Because reading `/proc/[pid]/smaps_rollup` requires the Linux kernel to traverse memory page tables, Pulse leaves PSS collection disabled by default (`--enable-pss=false`) to avoid its additional page-table scanning cost. When `--enable-pss=true` is enabled, Pulse safely parses `Pss:` and reports `N/A` without failure for processes where `smaps_rollup` is restricted by permissions (`hidepid`).

---

## 5. Measured Performance Benchmarks

Recorded benchmark environment: Pop!_OS 24.04, Linux amd64, AMD Ryzen 7 7700, Go 1.27.3.
This is the measurement toolchain, not the minimum installation requirement. The
measurements below are retained from that run; they have not been remeasured for
this documentation update.

```sh
go test -run '^$' -bench=. -benchtime=100ms -benchmem ./internal/procfs ./internal/engine
```

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Collector, 300 synthetic processes | 699025 | 2353765 | 6636 |
| Engine tick, 300 processes | 54985 | 215704 | 310 |

These are synthetic microbenchmarks, not guarantees of live overhead. No current
steady-state CPU overhead measurement is claimed. Snapshot isolation and locking
have costs; collection allocates temporary buffers per process.

---

## 6. Installation & Quick Start

### Requirements

* Linux with mounted `/proc`; ordinary user access is sufficient.
* Go 1.24.0 or later, plus Git. Both `go.mod` and the pinned Bubble Tea dependency require 1.24.0.
  CI checks `1.24.x` and `stable`; the benchmark toolchain was 1.27.3. These serve
  different purposes and do not require every user to install Go 1.27.3.
* A C compiler for race tests; static builds use `CGO_ENABLED=0`.
* An interactive terminal for the TUI.

### Build Static Binary

```zsh
# Clone repository
git clone https://github.com/khalid7344/pulse-linux-observatory.git
cd pulse-linux-observatory

# Compile static binary (CGO_ENABLED=0)
CGO_ENABLED=0 go build -o bin/pulse ./cmd/pulse

# Verify static linkage (no C runtime dependencies)
ldd bin/pulse
# Output: not a dynamic executable
```

### Quick Execution Modes

```zsh
# 1. Launch interactive Terminal UI
./bin/pulse

# 2. Launch with Irix CPU mode and PSS memory collection enabled
./bin/pulse --irix --enable-pss

# 3. Single-pass headless report (2 samples, 1 delta)
./bin/pulse --once

# 4. Single-pass JSON output (for scripting and pipelines)
./bin/pulse --once --json

# 5. Headless daemon running for 10 ticks, logging alerts to JSONL
./bin/pulse --headless --ticks=10 --alert-log=/tmp/pulse-alerts.jsonl
```

---

## 7. CLI Reference & Configuration

### CLI Flags

| Flag | Default | Description |
| :--- | :--- | :--- |
| `--proc-root` | `"/proc"` | Root path of the proc filesystem (points to synthetic test fixtures). |
| `--interval` | `1s` | Polling interval between `/proc` scans (set to `200ms` by `--once` unless `--interval` is explicit). |
| `--history-size` | `300` | Capacity of circular ring buffers in ticks (300 ticks = 5 min at 1Hz). |
| `--user-hz` | `100` | Kernel `USER_HZ` (`_SC_CLK_TCK`) for Irix delta calculation. |
| `--irix` | `false` | Start with Irix CPU mode (`100% = 1 core`) instead of Normalized mode. |
| `--enable-pss` | `false` | Attempt to read `/proc/[pid]/smaps_rollup` to populate `PSSBytes`. |
| `--cpu-warn` | `85.0` | System and single-process CPU% threshold for hysteresis alerts. |
| `--mem-warn-mb`| `2048` | Single-process RSS alert threshold in MiB (`2048 MiB = 2 GiB`). |
| `--sys-mem-warn`| `90.0` | System memory utilization % alert threshold. |
| `--zombie-warn` | `5` | Total zombie (`Z`) process count alert threshold. |
| `--sustained-ticks` | `3` | Consecutive ticks a threshold must be breached before firing an alert. |
| `--config` | `""` | Optional JSON config; explicit CLI flags override config; unknown fields fail. |
| `--alert-log` | `""` | Optional file path to append structured JSONL alert events. |
| `--once` | `false` | Take two samples separated by interval, print summary/JSON to stdout, and exit. |
| `--headless` | `false` | Run without TUI, emitting one-line tick summaries to stdout. |
| `--ticks` | `0` | Number of ticks to run in headless mode (`0` = run indefinitely until signal). |
| `--json` | `false` | Output snapshot as structured JSON (used in combination with `--once`). |

### JSON Configuration Example

Create `pulse-config.json`:

```json
{
  "engine": {
    "proc_root": "/proc",
    "interval": "1s",
    "history_size": 300,
    "user_hz": 100,
    "irix_mode": false,
    "enable_pss": false
  },
  "alert": {
    "cpu_warn_percent": 85.0,
    "mem_warn_mb": 2048,
    "sys_mem_warn_percent": 90.0,
    "zombie_warn_count": 5,
    "sustained_ticks": 3,
    "alert_log_path": "./pulse-alerts.jsonl"
  }
}
```

Run with configuration:
```zsh
./bin/pulse --config pulse-config.json
```

---

## 8. Interactive TUI Keybindings

| Key | Action |
| :--- | :--- |
| `c` | Sort processes by **CPU%** (toggles Ascending / Descending). |
| `m` | Sort processes by **Memory% / RSS** (toggles Ascending / Descending). |
| `p` | Sort processes by numeric **PID** (toggles Ascending / Descending). |
| `v` | Sort processes by **VMS** (Virtual Memory Size). |
| `i` | Toggle CPU mode between **NORMALIZED** (`0–100%`) and **IRIX** (`0–N*100%`). |
| `/` | Enter **substring filter mode** (filters by command name, arguments, or PID). |
| `Enter` | Confirm active filter and return to navigation. |
| `Esc` | Clear active filter and return to navigation. |
| `a` | Toggle **Alert Drawer** visibility. |
| `↑` / `k` | Scroll process table up. |
| `↓` / `j` | Scroll process table down. |
| `Home` / `g` | Jump to top of process table. |
| `End` / `G` | Jump to bottom of process table. |
| `q` / `Ctrl+C` | Cleanly exit Pulse Linux Observatory. |

---

## 9. Future Prometheus Integration (Phase 2 Blueprint)

Pulse defines a zero-dependency telemetry consumer boundary in `internal/model/sink.go`:

```go
type MetricsSink interface {
    Name() string
    OnSnapshot(ctx context.Context, view model.SystemView) error
    Close() error
}
```

In the current implementation, `internal/alert.Evaluator` implements `MetricsSink`. In Phase 2, a Prometheus exporter (`internal/prom.Exporter`) will implement `MetricsSink` without modifying core collection or delta engine layers.

### Phase 2 Metric Families
* `pulse_system_cpu_utilization_ratio`: System CPU utilization gauge (`0.0–1.0`).
* `pulse_system_memory_bytes{state="total|used|available|swap_used"}`: Memory gauges.
* `pulse_processes_by_state{state="R|S|D|Z|T"}`: Gauge breakdown of process states.
* `pulse_collector_scan_duration_seconds`: Collection pass latency gauge.
* `pulse_collector_skipped_total{reason="vanished|permission|parse_error"}`: Diagnostics counter.
* `pulse_top_process_cpu_ratio{comm="...",rank="1..10"}`: Bounded top-N CPU consumers.
* `pulse_top_process_memory_bytes{comm="...",type="rss|vms|pss",rank="1..10"}`: Bounded top-N memory consumers.

### Cardinality Safety Guardrail
> [!IMPORTANT]
> Linux Process IDs (`PID`) and ephemeral `StartTime` values must **never** be exported as unrestricted Prometheus metric labels. High process churn would rapidly create millions of short-lived time-series, leading to TSDB memory exhaustion. Phase 2 exports per-process metrics strictly aggregated by executable name (`comm`) or bounded to top-K ranks (`rank="1..10"`).

---

## 10. Verification Suite

Run all verification checks:

```zsh
# 1. Run all unit and integration tests under CGO_ENABLED=0
CGO_ENABLED=0 go test -v ./...

# 2. Concurrency race detection
go test -v -race ./...

# 3. Static analysis
go vet ./...

# 4. Performance benchmarks
CGO_ENABLED=0 go test -bench=. -benchmem ./...
```

---

## Supported environments and known limitations

Tested during this audit on Pop!_OS 24.04, Linux amd64, with Go 1.27.3, using
synthetic fixtures and a live `/proc` smoke run. Other distributions, architectures,
containers, and restricted proc mounts are unverified. The module minimum is
Go 1.24.0; [CI](https://github.com/khalid7344/pulse-linux-observatory/actions/workflows/ci.yml)
checks Go 1.24.x and stable Go. See the workflow for current results.

- Collection is a sequential, best-effort walk, not an atomic kernel snapshot.
  PID+starttime protects deltas across ticks; PID reuse during a single multi-file
  scan can still mix auxiliary fields. Unreadable optional fields fall back.
- Missing UID data currently displays as 0; it does not prove a process is root.
- RSS counts shared pages in each process. Optional PSS scans every readable PID,
  can cost substantially more, and reports N/A when unavailable.
- The first tick establishes a CPU baseline. Repeating an unchanged fixture yields
  0% CPU; exact changing-counter deltas are verified by two-fixture unit tests.
- USER_HZ defaults to 100; verify it for your host (`getconf CLK_TCK`) and override
  when needed. Host CPU accounting is not cgroup quota accounting.
- System and active-process history are bounded by `--history-size`; memory still
  scales with the visible process count. Alert history retains 100 events.
- Sink callbacks are synchronous; a slow sink or disk can delay the next scan.
  Custom sinks must serialize their own state if invoked concurrently.
- New alert logs use 0600; existing permissions are preserved. Logs are not rotated.
- Prometheus is a design proposal only: there is no exporter, HTTP endpoint, or stub
  production service. No released version is asserted by the TUI.

## Security and privacy

Run as your normal user. Do not elevate privileges just to inspect restricted
processes. Pulse opens no network listener and sends no telemetry. Process command
lines may contain secrets; `--json`, screenshots, and alert logs can expose process
names, UIDs, PIDs, and arguments. Keep live output private and review before sharing.
ASCII control characters from proc data are sanitized for terminal output. Proc
file reads are capped at 4 MiB; oversized required data is reported as an error.
Use trusted fixture directories and configuration files. Headless collection
errors exit nonzero instead of retrying forever.

## Demonstration

A genuine [TUI recording](docs/demo/fixture.typescript) uses only the checked-in
synthetic fixture. See [capture instructions](docs/demo/README.md). Reproduce:

```sh
./bin/pulse --proc-root=./testdata/proc_fixtures/tick_01 --once --enable-pss
./bin/pulse --proc-root=./testdata/proc_fixtures/tick_01 --headless --ticks=3 --interval=50ms
```

## Roadmap

Validate additional Linux environments; add explicit unknown
UID reporting, stronger within-scan PID identity checks, bounded asynchronous sinks,
log rotation, and the documented Prometheus exporter. Benchmark each change.

## License

Original project code is available under the [MIT License](LICENSE), as authorized
by its owner. Third-party dependencies retain their licenses and attributions;
see [dependency notices](docs/THIRD_PARTY_NOTICES.md). No third-party code is relicensed.

## Troubleshooting

- **Missing /proc/stat or /proc/meminfo:** run on Linux with a readable proc mount,
  or use `--proc-root=testdata/proc_fixtures/tick_01` for a synthetic demonstration.
- **0% CPU in the fixture demo:** the unchanged fixture has no counter deltas; the
  two-fixture tests verify nonzero utilization. The first live sample is a baseline.
- **PSS is N/A:** leave PSS disabled or accept missing values when proc permissions
  restrict access; elevated privileges are not required for normal operation.
- **No interactive terminal:** use `--once --json` or `--headless --ticks=3`.
- **Config rejected:** use the nested `engine`/`alert` JSON example above, positive
  timing/history values and valid threshold ranges. Unknown fields are rejected.
- **Go build fails from an exported source archive:** add `-buildvcs=false` when
  the surrounding directory cannot supply valid Git metadata.

For reproducible local static builds, use the same Go toolchain and source:

```sh
CGO_ENABLED=0 go build -trimpath -buildvcs=false -o bin/pulse ./cmd/pulse
```
