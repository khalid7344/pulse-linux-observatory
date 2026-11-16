package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/khalid7344/pulse-linux-observatory/internal/alert"
	"github.com/khalid7344/pulse-linux-observatory/internal/engine"
	"github.com/khalid7344/pulse-linux-observatory/internal/model"
	"github.com/khalid7344/pulse-linux-observatory/internal/procfs"
	"github.com/khalid7344/pulse-linux-observatory/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pulse", flag.ContinueOnError)

	procRoot := fs.String("proc-root", "/proc", "Root path of the proc filesystem")
	interval := fs.Duration("interval", 1*time.Second, "Polling interval between /proc scans")
	historySize := fs.Int("history-size", 300, "Capacity of in-memory circular ring buffers (ticks)")
	userHZ := fs.Uint64("user-hz", 100, "Kernel USER_HZ (_SC_CLK_TCK) value used for Irix CPU% math")
	irix := fs.Bool("irix", false, "Start with Irix CPU% mode (100% = 1 core) instead of Normalized mode")
	enablePSS := fs.Bool("enable-pss", false, "Attempt to read /proc/[pid]/smaps_rollup to populate PSSBytes")
	cpuWarn := fs.Float64("cpu-warn", 85.0, "System/Process CPU% alert threshold")
	memWarnMB := fs.Uint64("mem-warn-mb", 2048, "Single-process RSS alert threshold in MiB")
	sysMemWarn := fs.Float64("sys-mem-warn", 90.0, "System Memory% alert threshold")
	zombieWarn := fs.Int("zombie-warn", 5, "Zombie process count alert threshold")
	sustainedTicks := fs.Int("sustained-ticks", 3, "Consecutive ticks threshold must be breached before firing")
	configPath := fs.String("config", "", "Optional path to JSON configuration file overriding defaults")
	alertLog := fs.String("alert-log", "", "Optional file path to append structured JSONL alert events")
	once := fs.Bool("once", false, "Perform two samples, print single summary to stdout, and exit 0")
	headless := fs.Bool("headless", false, "Run without TUI for --ticks N iterations")
	ticks := fs.Int("ticks", 0, "Number of ticks to run in headless mode (0 = run until signal)")
	jsonOutput := fs.Bool("json", false, "Output snapshot as JSON (useful with --once)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Track which flags were explicitly set
	explicitFlags := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) {
		explicitFlags[f.Name] = true
	})

	// 1. Load config file if provided
	cfg := model.DefaultConfig()
	if *configPath != "" {
		loadedCfg, err := model.LoadConfig(*configPath)
		if err != nil {
			return fmt.Errorf("load config %q: %w", *configPath, err)
		}
		cfg = loadedCfg
	}

	// 2. Override with explicit CLI flags
	if explicitFlags["proc-root"] {
		cfg.Engine.ProcRoot = *procRoot
	}
	if explicitFlags["interval"] {
		cfg.Engine.Interval = *interval
	}
	if explicitFlags["history-size"] {
		cfg.Engine.HistorySize = *historySize
	}
	if explicitFlags["user-hz"] {
		cfg.Engine.UserHZ = *userHZ
	}
	if explicitFlags["irix"] {
		cfg.Engine.IrixMode = *irix
	}
	if explicitFlags["enable-pss"] {
		cfg.Engine.EnablePSS = *enablePSS
	}
	if explicitFlags["cpu-warn"] {
		cfg.Alert.CPUWarnPercent = *cpuWarn
	}
	if explicitFlags["mem-warn-mb"] {
		cfg.Alert.MemWarnMB = *memWarnMB
	}
	if explicitFlags["sys-mem-warn"] {
		cfg.Alert.SysMemWarnPercent = *sysMemWarn
	}
	if explicitFlags["zombie-warn"] {
		cfg.Alert.ZombieWarnCount = *zombieWarn
	}
	if explicitFlags["sustained-ticks"] {
		cfg.Alert.SustainedTicks = *sustainedTicks
	}
	if explicitFlags["alert-log"] {
		cfg.Alert.AlertLogPath = *alertLog
	}

	// If --once is set and interval was not explicitly set, default interval to 200ms
	if *once && !explicitFlags["interval"] {
		cfg.Engine.Interval = 200 * time.Millisecond
	}

	if fs.NArg() != 0 || *ticks < 0 || (*ticks > 0 && !*headless) || (*jsonOutput && !*once) || (*once && *headless) {
		return fmt.Errorf("invalid mode combination, ticks, or positional arguments")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}

	// 3. Initialize components
	collector := procfs.NewHostCollector(cfg.Engine.ProcRoot, cfg.Engine.PageSize, cfg.Engine.EnablePSS)
	eng := engine.NewEngine(collector, cfg.Engine)
	defer eng.Close()

	alertEval, err := alert.NewEvaluator(cfg.Alert)
	if err != nil {
		return fmt.Errorf("init alert evaluator: %w", err)
	}
	eng.RegisterSink(alertEval)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// 4. Mode Routing: --once vs --headless vs interactive TUI
	if *once {
		return runOnce(ctx, eng, alertEval, cfg.Engine.Interval, *jsonOutput)
	}

	if *headless {
		return runHeadless(ctx, eng, alertEval, cfg.Engine.Interval, *ticks)
	}

	return runInteractive(ctx, eng, alertEval, cfg.Engine.Interval)
}

func runOnce(ctx context.Context, eng *engine.Engine, alertEval *alert.Evaluator, interval time.Duration, jsonOut bool) error {
	// Sample 1: Baseline
	_, err := eng.Tick(ctx)
	if err != nil {
		return fmt.Errorf("baseline sample: %w", err)
	}

	// Wait for sample interval
	select {
	case <-time.After(interval):
	case <-ctx.Done():
		return ctx.Err()
	}

	// Sample 2: Deltas
	view, err := eng.Tick(ctx)
	if err != nil {
		return fmt.Errorf("delta sample: %w", err)
	}

	view.ActiveAlerts = alertEval.ActiveAlerts()

	if jsonOut {
		data, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal json view: %w", err)
		}
		fmt.Println(string(data))
		return nil
	}

	// Formatted human-readable report
	fmt.Println("=== PULSE LINUX OBSERVATORY — ONCE REPORT ===")
	fmt.Printf("Tick: %d | Time: %s | Interval: %v | Cores: %d | USER_HZ: %d\n",
		view.TickNumber, view.Timestamp.Format(time.RFC3339), view.SampleInterval, view.NumCores, view.UserHZ)
	fmt.Printf("System CPU: %.2f%%\n", view.CPUPercent)
	fmt.Printf("Memory: %s / %s (%.1f%%) | Available: %s | Swap Used: %s\n",
		tui.FormatBytes(view.MemUsedBytes),
		tui.FormatBytes(view.MemTotalBytes),
		view.MemPercent,
		tui.FormatBytes(view.MemAvailableBytes),
		tui.FormatBytes(view.SwapUsedBytes))
	fmt.Printf("Processes: %d Total, %d Running, %d Zombies | Scan Duration: %v\n",
		view.TotalProcs, view.RunningProcs, view.ZombieCount, view.ScanDuration)

	if len(view.ActiveAlerts) > 0 {
		fmt.Println("\nActive Alerts:")
		for _, a := range view.ActiveAlerts {
			fmt.Printf("  - [%s] %s (State: %s)\n", a.RuleName, a.Message, a.State)
		}
	}

	// Top 10 processes by Normalized CPU%
	sortedProcs := make([]model.ProcessMetrics, len(view.Processes))
	copy(sortedProcs, view.Processes)
	sort.Slice(sortedProcs, func(i, j int) bool {
		return sortedProcs[i].CPUPercentNorm > sortedProcs[j].CPUPercentNorm
	})

	limit := 10
	if len(sortedProcs) < limit {
		limit = len(sortedProcs)
	}

	fmt.Printf("\nTop %d Processes by CPU%%:\n", limit)
	fmt.Printf("%-7s %-6s %-3s %-8s %-8s %-10s %-10s %-10s %s\n",
		"PID", "USER", "S", "CPU%NORM", "CPU%IRIX", "RSS", "VMS", "PSS", "COMMAND")
	for i := 0; i < limit; i++ {
		p := sortedProcs[i]
		pssStr := "N/A"
		if p.PSSValid {
			pssStr = tui.FormatBytes(p.PSSBytes)
		}
		cmdStr := p.Cmdline
		if cmdStr == "" {
			cmdStr = "[" + p.Comm + "]"
		}
		if len(cmdStr) > 40 {
			cmdStr = cmdStr[:37] + "..."
		}
		fmt.Printf("%-7d %-6d %-3s %7.1f%% %7.1f%% %-10s %-10s %-10s %s\n",
			p.Key.PID, p.UID, p.State, p.CPUPercentNorm, p.CPUPercentIrix,
			tui.FormatBytes(p.RSSBytes), tui.FormatBytes(p.VMSBytes), pssStr, cmdStr)
	}
	return nil
}

func runHeadless(ctx context.Context, eng *engine.Engine, alertEval *alert.Evaluator, interval time.Duration, maxTicks int) error {
	fmt.Println("Starting Pulse Linux Observatory in HEADLESS mode...")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	tickCount := 0
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nReceived termination signal. Shutting down headless runner...")
			return nil
		case <-ticker.C:
			tickCount++
			view, err := eng.Tick(ctx)
			if err != nil {
				return fmt.Errorf("headless tick: %w", err)
			}

			active := alertEval.ActiveAlerts()
			alertSummary := ""
			if len(active) > 0 {
				alertSummary = fmt.Sprintf(" | 🚨 %d ALERTS FIRING", len(active))
			}

			fmt.Printf("[Tick #%d] CPU: %5.1f%% | Mem: %5.1f%% (%s / %s) | Procs: %d (Zombies: %d) | Scan: %v%s\n",
				view.TickNumber, view.CPUPercent, view.MemPercent,
				tui.FormatBytes(view.MemUsedBytes), tui.FormatBytes(view.MemTotalBytes),
				view.TotalProcs, view.ZombieCount, view.ScanDuration, alertSummary)

			if maxTicks > 0 && tickCount >= maxTicks {
				fmt.Printf("Reached target %d ticks. Headless run complete.\n", maxTicks)
				return nil
			}
		}
	}
}

func runInteractive(ctx context.Context, eng *engine.Engine, alertEval *alert.Evaluator, interval time.Duration) error {
	app := tui.NewApp(eng, alertEval, interval)
	app.SetContext(ctx)
	prog := tea.NewProgram(app, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := prog.Run()
	if err != nil {
		return err
	}
	return app.Err()
}
