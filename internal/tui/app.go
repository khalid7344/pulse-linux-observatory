package tui

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/khalid7344/pulse-linux-observatory/internal/alert"
	"github.com/khalid7344/pulse-linux-observatory/internal/engine"
	"github.com/khalid7344/pulse-linux-observatory/internal/model"
)

type SortField int

const (
	SortCPU SortField = iota
	SortMem
	SortPID
	SortVMS
)

// SnapshotMsg carries a new telemetry view and alert state to the Bubble Tea loop
type SnapshotMsg struct {
	View         model.SystemView
	ActiveAlerts []model.AlertEvent
	RecentAlerts []model.AlertEvent
	Err          error
}

// TickMsg triggers the next telemetry collection tick
type TickMsg time.Time

// App represents the Bubble Tea interactive TUI model.
type App struct {
	ctx           context.Context
	err           error
	engine        *engine.Engine
	alertEval     *alert.Evaluator
	interval      time.Duration
	width         int
	height        int
	cursor        int
	scrollOffset  int
	sortField     SortField
	sortAsc       bool
	filterMode    bool
	filterText    string
	showAlerts    bool
	showHelp      bool
	view          *model.SystemView
	activeAlerts  []model.AlertEvent
	recentAlerts  []model.AlertEvent
	irixMode      bool
	quitting      bool
	filteredProcs []model.ProcessMetrics
}

// NewApp initializes a new TUI application.
func NewApp(eng *engine.Engine, alertEval *alert.Evaluator, interval time.Duration) *App {
	if interval <= 0 {
		interval = 1 * time.Second
	}
	irix := false
	if eng != nil {
		irix = eng.IrixMode()
	}

	return &App{
		ctx:        context.Background(),
		engine:     eng,
		alertEval:  alertEval,
		interval:   interval,
		sortField:  SortCPU,
		sortAsc:    false, // default sort CPU descending
		showAlerts: true,
		irixMode:   irix,
		width:      100,
		height:     30,
	}
}

// Init begins the Bubble Tea event loop with an immediate tick.
func (a *App) Init() tea.Cmd {
	return a.doTick()
}

func (a *App) doTick() tea.Cmd {
	return tea.Tick(a.interval, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// collectSnapshot executes an engine tick and fetches alerts
func (a *App) collectSnapshot() tea.Cmd {
	return func() tea.Msg {
		if a.engine == nil {
			return SnapshotMsg{}
		}
		view, err := a.engine.Tick(a.ctx)
		var active []model.AlertEvent
		var recent []model.AlertEvent
		if a.alertEval != nil {
			active = a.alertEval.ActiveAlerts()
			recent = a.alertEval.RecentAlerts()
		}
		return SnapshotMsg{
			View:         view,
			ActiveAlerts: active,
			RecentAlerts: recent,
			Err:          err,
		}
	}
}

// Update handles incoming terminal messages, keypresses, and telemetry ticks.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = msg.Width
		a.height = msg.Height
		return a, nil

	case TickMsg:
		return a, a.collectSnapshot()

	case SnapshotMsg:
		if msg.Err != nil {
			a.err = msg.Err
			a.quitting = true
			return a, tea.Quit
		}
		if msg.Err == nil {
			viewCopy := msg.View
			a.view = &viewCopy
			a.activeAlerts = msg.ActiveAlerts
			a.recentAlerts = msg.RecentAlerts
			a.recalculateFilteredProcs()
		}
		return a, a.doTick()

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			a.quitting = true
			return a, tea.Quit
		}
		if a.filterMode {
			switch msg.Type {
			case tea.KeyEnter:
				a.filterMode = false
				a.cursor = 0
				a.scrollOffset = 0
				return a, nil
			case tea.KeyEsc:
				a.filterMode = false
				a.filterText = ""
				a.cursor = 0
				a.scrollOffset = 0
				a.recalculateFilteredProcs()
				return a, nil
			case tea.KeyBackspace:
				if len(a.filterText) > 0 {
					runes := []rune(a.filterText)
					a.filterText = string(runes[:len(runes)-1])
					a.cursor = 0
					a.scrollOffset = 0
					a.recalculateFilteredProcs()
				}
				return a, nil
			case tea.KeyRunes:
				a.filterText += string(msg.Runes)
				a.cursor = 0
				a.scrollOffset = 0
				a.recalculateFilteredProcs()
				return a, nil
			}
			return a, nil
		}

		// Normal navigation and command keys
		switch msg.String() {
		case "q", "ctrl+c":
			a.quitting = true
			return a, tea.Quit

		case "c":
			if a.sortField == SortCPU {
				a.sortAsc = !a.sortAsc
			} else {
				a.sortField = SortCPU
				a.sortAsc = false
			}
			a.cursor = 0
			a.recalculateFilteredProcs()

		case "m":
			if a.sortField == SortMem {
				a.sortAsc = !a.sortAsc
			} else {
				a.sortField = SortMem
				a.sortAsc = false
			}
			a.cursor = 0
			a.recalculateFilteredProcs()

		case "p":
			if a.sortField == SortPID {
				a.sortAsc = !a.sortAsc
			} else {
				a.sortField = SortPID
				a.sortAsc = true
			}
			a.cursor = 0
			a.recalculateFilteredProcs()

		case "v":
			if a.sortField == SortVMS {
				a.sortAsc = !a.sortAsc
			} else {
				a.sortField = SortVMS
				a.sortAsc = false
			}
			a.cursor = 0
			a.recalculateFilteredProcs()

		case "i":
			a.irixMode = !a.irixMode
			if a.engine != nil {
				a.engine.SetIrixMode(a.irixMode)
			}
			a.recalculateFilteredProcs()

		case "/":
			a.filterMode = true

		case "a":
			a.showAlerts = !a.showAlerts

		case "?":
			a.showHelp = !a.showHelp

		case "up", "k":
			if a.cursor > 0 {
				a.cursor--
				if a.cursor < a.scrollOffset {
					a.scrollOffset = a.cursor
				}
			}

		case "down", "j":
			maxIdx := len(a.filteredProcs) - 1
			if a.cursor < maxIdx {
				a.cursor++
				visibleRows := a.tableVisibleRows()
				if a.cursor >= a.scrollOffset+visibleRows {
					a.scrollOffset = a.cursor - visibleRows + 1
				}
			}

		case "home", "g":
			a.cursor = 0
			a.scrollOffset = 0

		case "end", "G":
			maxIdx := len(a.filteredProcs) - 1
			if maxIdx >= 0 {
				a.cursor = maxIdx
				visibleRows := a.tableVisibleRows()
				if a.cursor >= visibleRows {
					a.scrollOffset = a.cursor - visibleRows + 1
				} else {
					a.scrollOffset = 0
				}
			}
		}
	}

	return a, nil
}

func (a *App) tableVisibleRows() int {
	// Approximate space available for process table
	reservedLines := 14
	if a.showAlerts && (len(a.activeAlerts) > 0 || len(a.recentAlerts) > 0) {
		reservedLines += 4
	}
	avail := a.height - reservedLines
	if avail < 5 {
		avail = 5
	}
	return avail
}

// recalculateFilteredProcs sorts and filters processes from the current view
func (a *App) recalculateFilteredProcs() {
	if a.view == nil {
		a.filteredProcs = nil
		return
	}

	procs := make([]model.ProcessMetrics, len(a.view.Processes))
	copy(procs, a.view.Processes)

	// Filter
	if a.filterText != "" {
		filtered := make([]model.ProcessMetrics, 0, len(procs))
		lowerFilter := strings.ToLower(a.filterText)
		for _, p := range procs {
			pidStr := strconv.Itoa(p.Key.PID)
			if strings.Contains(strings.ToLower(p.Comm), lowerFilter) ||
				strings.Contains(strings.ToLower(p.Cmdline), lowerFilter) ||
				strings.Contains(pidStr, lowerFilter) {
				filtered = append(filtered, p)
			}
		}
		procs = filtered
	}

	// Sort
	sort.Slice(procs, func(i, j int) bool {
		switch a.sortField {
		case SortCPU:
			var valI, valJ float64
			if a.irixMode {
				valI, valJ = procs[i].CPUPercentIrix, procs[j].CPUPercentIrix
			} else {
				valI, valJ = procs[i].CPUPercentNorm, procs[j].CPUPercentNorm
			}
			if a.sortAsc {
				return valI < valJ
			}
			return valI > valJ

		case SortMem:
			if a.sortAsc {
				return procs[i].RSSBytes < procs[j].RSSBytes
			}
			return procs[i].RSSBytes > procs[j].RSSBytes

		case SortPID:
			if a.sortAsc {
				return procs[i].Key.PID < procs[j].Key.PID
			}
			return procs[i].Key.PID > procs[j].Key.PID

		case SortVMS:
			if a.sortAsc {
				return procs[i].VMSBytes < procs[j].VMSBytes
			}
			return procs[i].VMSBytes > procs[j].VMSBytes

		default:
			return procs[i].CPUPercentNorm > procs[j].CPUPercentNorm
		}
	})

	a.filteredProcs = procs
	if a.scrollOffset >= len(procs) {
		a.scrollOffset = 0
	}
	if a.cursor >= len(a.filteredProcs) {
		a.cursor = len(a.filteredProcs) - 1
		if a.cursor < 0 {
			a.cursor = 0
		}
	}
}

// View renders the full terminal UI layout.
func (a *App) View() string {
	if a.quitting {
		return "Exiting Pulse Linux Observatory...\n"
	}

	var sb strings.Builder

	// 1. Header Title & System Summary
	title := titleStyle.Render("⚡ PULSE LINUX OBSERVATORY")
	modeLabel := "[NORMALIZED 0-100%]"
	if a.irixMode {
		modeLabel = "[IRIX 0-N*100%]"
	}

	var tickInfo string
	if a.view != nil {
		tickInfo = fmt.Sprintf("Tick #%d | Interval: %v | Cores: %d | USER_HZ: %d | Scan: %v",
			a.view.TickNumber, a.view.SampleInterval, a.view.NumCores, a.view.UserHZ, a.view.ScanDuration)
	} else {
		tickInfo = "Initializing /proc collector..."
	}

	headerTop := lipgloss.JoinHorizontal(lipgloss.Top,
		title,
		"  ",
		subtitleStyle.Render(modeLabel),
		"  ",
		subtitleStyle.Render(tickInfo),
	)
	sb.WriteString(headerTop + "\n\n")

	if a.view == nil {
		sb.WriteString("Waiting for initial telemetry sample...\n")
		return sb.String()
	}

	// 2. System Gauges & Sparklines
	view := a.view

	// Extract history points for sparklines
	cpuHistory := make([]float64, len(view.History))
	memHistory := make([]float64, len(view.History))
	for i, h := range view.History {
		cpuHistory[i] = h.CPUPercent
		memHistory[i] = h.MemPercent
	}

	sparkWidth := 20
	if a.width > 120 {
		sparkWidth = 35
	}
	cpuSpark := RenderSparkline(cpuHistory, 100.0, sparkWidth)
	memSpark := RenderSparkline(memHistory, 100.0, sparkWidth)

	cpuGauge := RenderGauge(view.CPUPercent, 18)
	memGauge := RenderGauge(view.MemPercent, 18)

	memUsedStr := FormatBytes(view.MemUsedBytes)
	memTotalStr := FormatBytes(view.MemTotalBytes)
	swapUsedStr := FormatBytes(view.SwapUsedBytes)
	swapTotalStr := FormatBytes(view.SwapTotalBytes)

	cpuLine := fmt.Sprintf("%s %s  Trend: [%s]",
		headerLabelStyle.Render("CPU:"),
		cpuGauge,
		lipgloss.NewStyle().Foreground(colorAccent).Render(cpuSpark),
	)

	memLine := fmt.Sprintf("%s %s  %s / %s  Swap: %s / %s  Trend: [%s]",
		headerLabelStyle.Render("MEM:"),
		memGauge,
		memUsedStr, memTotalStr,
		swapUsedStr, swapTotalStr,
		lipgloss.NewStyle().Foreground(colorAccent).Render(memSpark),
	)

	procsLine := fmt.Sprintf("%s %s %s  |  %s %s  |  %s %s",
		headerLabelStyle.Render("Processes:"),
		headerValStyle.Render(strconv.Itoa(view.TotalProcs)),
		subtitleStyle.Render(fmt.Sprintf("(Running: %d)", view.RunningProcs)),
		headerLabelStyle.Render("Zombies:"),
		func() string {
			if view.ZombieCount > 0 {
				return alertFiringStyle.Render(strconv.Itoa(view.ZombieCount))
			}
			return headerValStyle.Render(strconv.Itoa(view.ZombieCount))
		}(),
		headerLabelStyle.Render("Diagnostics:"),
		subtitleStyle.Render(fmt.Sprintf("Vanished: %d | Perm: %d | Errors: %d",
			view.SkippedVanished, view.SkippedPermission, view.ParseErrors)),
	)

	sb.WriteString(cpuLine + "\n")
	sb.WriteString(memLine + "\n")
	sb.WriteString(procsLine + "\n\n")

	// 3. Active Alert Banner / Drawer
	if a.showAlerts && (len(a.activeAlerts) > 0 || len(a.recentAlerts) > 0) {
		var alertLines []string
		if len(a.activeAlerts) > 0 {
			for _, alert := range a.activeAlerts {
				alertLines = append(alertLines, alertFiringStyle.Render(fmt.Sprintf("🚨 FIRING: [%s] %s", alert.RuleName, alert.Message)))
			}
		}
		if len(alertLines) == 0 && len(a.recentAlerts) > 0 {
			// Show last resolved alert if no active
			last := a.recentAlerts[len(a.recentAlerts)-1]
			if last.State == "RESOLVED" {
				alertLines = append(alertLines, alertResolvedStyle.Render(fmt.Sprintf("✓ RESOLVED: [%s] %s", last.RuleName, last.Message)))
			}
		}
		if len(alertLines) > 0 {
			alertContent := strings.Join(alertLines, " | ")
			sb.WriteString(alertBoxStyle.Render(alertContent) + "\n\n")
		}
	}

	// 4. Filter Input Bar
	if a.filterMode || a.filterText != "" {
		filterPrompt := "FILTER: "
		if a.filterMode {
			filterPrompt = "FILTER (Type to filter, Enter to confirm, Esc to clear): "
		}
		sb.WriteString(filterPromptStyle.Render(filterPrompt) +
			lipgloss.NewStyle().Background(colorHighlight).Foreground(colorFg).Render(a.filterText) + "\n")
	}

	// 5. Process Table
	cpuHeader := "CPU%"
	if a.sortField == SortCPU {
		if a.sortAsc {
			cpuHeader += " ▲"
		} else {
			cpuHeader += " ▼"
		}
	}
	memHeader := "MEM%"
	if a.sortField == SortMem {
		if a.sortAsc {
			memHeader += " ▲"
		} else {
			memHeader += " ▼"
		}
	}
	pidHeader := "PID"
	if a.sortField == SortPID {
		if a.sortAsc {
			pidHeader += " ▲"
		} else {
			pidHeader += " ▼"
		}
	}
	vmsHeader := "VMS"
	if a.sortField == SortVMS {
		if a.sortAsc {
			vmsHeader += " ▲"
		} else {
			vmsHeader += " ▼"
		}
	}

	tableHeader := fmt.Sprintf("%-7s %-6s %-3s %-8s %-8s %-10s %-10s %-10s %-4s %s",
		pidHeader, "USER", "S", cpuHeader, memHeader, "RSS", vmsHeader, "PSS", "TH", "COMMAND")
	sb.WriteString(tableHeaderStyle.Render(tableHeader) + "\n")

	visibleRows := a.tableVisibleRows()
	procs := a.filteredProcs

	endIdx := a.scrollOffset + visibleRows
	if endIdx > len(procs) {
		endIdx = len(procs)
	}

	for i := a.scrollOffset; i < endIdx; i++ {
		p := procs[i]

		var cpuVal float64
		if a.irixMode {
			cpuVal = p.CPUPercentIrix
		} else {
			cpuVal = p.CPUPercentNorm
		}

		pssStr := "N/A"
		if p.PSSValid {
			pssStr = FormatBytes(p.PSSBytes)
		}

		userStr := strconv.FormatUint(uint64(p.UID), 10)
		cmdStr := p.Cmdline
		if cmdStr == "" {
			cmdStr = "[" + p.Comm + "]"
		}

		// Truncate command to fit terminal width
		maxCmdLen := a.width - 72
		if maxCmdLen < 15 {
			maxCmdLen = 15
		}
		if len(cmdStr) > maxCmdLen {
			cmdStr = cmdStr[:maxCmdLen-3] + "..."
		}

		row := fmt.Sprintf("%-7d %-6s %-3s %7.1f%% %7.1f%% %-10s %-10s %-10s %-4d %s",
			p.Key.PID,
			userStr,
			p.State,
			cpuVal,
			p.MemPercent,
			FormatBytes(p.RSSBytes),
			FormatBytes(p.VMSBytes),
			pssStr,
			p.NumThreads,
			cmdStr,
		)

		if i == a.cursor {
			sb.WriteString(selectedRowStyle.Render(row) + "\n")
		} else {
			sb.WriteString(normalRowStyle.Render(row) + "\n")
		}
	}

	// 6. Footer Navigation Bar
	footer := fmt.Sprintf("Scroll: ↑/↓/j/k | Sort: [c]CPU [m]MEM [p]PID [v]VMS | Mode: [i]Irix/Norm | Filter: [/] | Alerts: [a] | Quit: [q]")
	sb.WriteString("\n" + subtitleStyle.Render(footer))

	return sb.String()
}

// SetContext propagates application cancellation to collection.
func (a *App) SetContext(ctx context.Context) { a.ctx = ctx }

// Err reports a collection failure that stopped the TUI.
func (a *App) Err() error { return a.err }
