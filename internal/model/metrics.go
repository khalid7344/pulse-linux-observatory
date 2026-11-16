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

// AlertEvent represents a transition in an alert's lifecycle.
type AlertEvent struct {
	ID         string     `json:"id"`
	RuleName   string     `json:"rule_name"`
	Severity   string     `json:"severity"`
	State      string     `json:"state"` // "FIRING", "RESOLVED"
	Message    string     `json:"message"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Value      float64    `json:"value"`
	Threshold  float64    `json:"threshold"`
	PID        int        `json:"pid,omitempty"`
	Comm       string     `json:"comm,omitempty"`
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
