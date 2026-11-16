package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

// EngineConfig holds configuration for the telemetry collection engine.
type EngineConfig struct {
	ProcRoot    string        `json:"proc_root"`
	Interval    time.Duration `json:"interval"`
	HistorySize int           `json:"history_size"`
	UserHZ      uint64        `json:"user_hz"`
	IrixMode    bool          `json:"irix_mode"`
	EnablePSS   bool          `json:"enable_pss"`
	PageSize    uint64        `json:"page_size"`
}

// AlertConfig holds configuration for the alerting engine and hysteresis rules.
type AlertConfig struct {
	CPUWarnPercent    float64 `json:"cpu_warn_percent"`
	MemWarnMB         uint64  `json:"mem_warn_mb"`
	SysMemWarnPercent float64 `json:"sys_mem_warn_percent"`
	ZombieWarnCount   int     `json:"zombie_warn_count"`
	SustainedTicks    int     `json:"sustained_ticks"`
	AlertLogPath      string  `json:"alert_log_path"`
}

// Config bundles all configuration options for Pulse Linux Observatory.
type Config struct {
	Engine EngineConfig `json:"engine"`
	Alert  AlertConfig  `json:"alert"`
}

// DefaultConfig returns the default configuration according to the specification.
func DefaultConfig() Config {
	pageSize := uint64(4096)
	if ps := os.Getpagesize(); ps > 0 {
		pageSize = uint64(ps)
	}
	return Config{
		Engine: EngineConfig{
			ProcRoot:    "/proc",
			Interval:    1 * time.Second,
			HistorySize: 300,
			UserHZ:      100,
			IrixMode:    false,
			EnablePSS:   false,
			PageSize:    pageSize,
		},
		Alert: AlertConfig{
			CPUWarnPercent:    85.0,
			MemWarnMB:         2048, // 2048 MiB = 2 GiB
			SysMemWarnPercent: 90.0,
			ZombieWarnCount:   5,
			SustainedTicks:    3,
			AlertLogPath:      "",
		},
	}
}

// LoadConfig reads and parses a JSON configuration file, merging values onto defaults.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config file: %w", err)
	}

	// We decode into an intermediate struct or directly into Config
	// Note: time.Duration in JSON might be a string like "1s" or an integer ns.
	// To support string duration in JSON, we can use an unmarshaler or helper.
	type rawEngineConfig struct {
		ProcRoot    *string          `json:"proc_root"`
		Interval    *json.RawMessage `json:"interval"`
		HistorySize *int             `json:"history_size"`
		UserHZ      *uint64          `json:"user_hz"`
		IrixMode    *bool            `json:"irix_mode"`
		EnablePSS   *bool            `json:"enable_pss"`
		PageSize    *uint64          `json:"page_size"`
	}
	type rawConfig struct {
		Engine *rawEngineConfig `json:"engine"`
		Alert  *AlertConfig     `json:"alert"`
	}

	if trimmed := bytes.TrimSpace(data); len(trimmed) == 0 || trimmed[0] != '{' {
		return cfg, fmt.Errorf("config must be a JSON object")
	}
	raw := rawConfig{Alert: &cfg.Alert}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return cfg, fmt.Errorf("unmarshal config json: %w", err)
	}

	if err := decoder.Decode(new(any)); err != io.EOF {
		return cfg, fmt.Errorf("config must contain one JSON object")
	}
	if raw.Engine != nil {
		if raw.Engine.ProcRoot != nil {
			cfg.Engine.ProcRoot = *raw.Engine.ProcRoot
		}
		if raw.Engine.Interval != nil {
			// Try parsing as string first, then as integer nanoseconds
			var s string
			if err := json.Unmarshal(*raw.Engine.Interval, &s); err == nil {
				d, err := time.ParseDuration(s)
				if err != nil {
					return cfg, fmt.Errorf("invalid interval format %q: %w", s, err)
				}
				cfg.Engine.Interval = d
			} else {
				var n int64
				if err := json.Unmarshal(*raw.Engine.Interval, &n); err == nil {
					cfg.Engine.Interval = time.Duration(n)
				} else {
					return cfg, fmt.Errorf("invalid interval format: %s", string(*raw.Engine.Interval))
				}
			}
		}
		if raw.Engine.HistorySize != nil {
			cfg.Engine.HistorySize = *raw.Engine.HistorySize
		}
		if raw.Engine.UserHZ != nil {
			cfg.Engine.UserHZ = *raw.Engine.UserHZ
		}
		if raw.Engine.IrixMode != nil {
			cfg.Engine.IrixMode = *raw.Engine.IrixMode
		}
		if raw.Engine.EnablePSS != nil {
			cfg.Engine.EnablePSS = *raw.Engine.EnablePSS
		}
		if raw.Engine.PageSize != nil {
			cfg.Engine.PageSize = *raw.Engine.PageSize
		}
	}

	return cfg, nil
}

// Validate rejects invalid values before timers, allocations, or files are created.
func (c Config) Validate() error {
	if c.Engine.ProcRoot == "" || c.Engine.Interval <= 0 || c.Engine.HistorySize <= 0 || c.Engine.HistorySize > 100000 || c.Engine.UserHZ == 0 {
		return fmt.Errorf("engine requires a proc root, positive interval/user-hz and history-size in 1..100000")
	}
	for _, v := range []float64{c.Alert.CPUWarnPercent, c.Alert.SysMemWarnPercent} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 || v > 100 {
			return fmt.Errorf("CPU and system memory thresholds must be finite and in (0,100]")
		}
	}
	if c.Alert.MemWarnMB == 0 || c.Alert.ZombieWarnCount < 0 || c.Alert.SustainedTicks <= 0 {
		return fmt.Errorf("RSS and sustained-ticks must be positive; zombie-warn must be nonnegative")
	}
	return nil
}
