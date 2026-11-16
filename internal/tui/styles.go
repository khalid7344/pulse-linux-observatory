package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var (
	// Palette
	colorBrand     = lipgloss.Color("#7D56F4")
	colorAccent    = lipgloss.Color("#00D7D7")
	colorSuccess   = lipgloss.Color("#50FA7B")
	colorWarning   = lipgloss.Color("#FFB86C")
	colorDanger    = lipgloss.Color("#FF5555")
	colorMuted     = lipgloss.Color("#6272A4")
	colorFg        = lipgloss.Color("#F8F8F2")
	colorBgDark    = lipgloss.Color("#21222C")
	colorHighlight = lipgloss.Color("#44475A")

	// Base Box Styles
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBrand).
			MarginBottom(0)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBrand).
			Padding(0, 1)

	headerLabelStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)

	headerValStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorFg)

	// Table Styles
	tableHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent).
				Border(lipgloss.NormalBorder(), false, false, true, false).
				BorderForeground(colorMuted)

	selectedRowStyle = lipgloss.NewStyle().
				Background(colorHighlight).
				Bold(true).
				Foreground(colorFg)

	normalRowStyle = lipgloss.NewStyle().
			Foreground(colorFg)

	// Alert Styles
	alertFiringStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorDanger)

	alertResolvedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorSuccess)

	alertBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorWarning).
			Padding(0, 1).
			MarginTop(0)

	filterPromptStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(colorAccent)
)

// RenderGauge creates an ASCII/Unicode progress bar: [██████░░░░] 60%
func RenderGauge(percent float64, width int) string {
	if width <= 0 {
		width = 10
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filledLen := int((percent / 100.0) * float64(width))
	if filledLen > width {
		filledLen = width
	}
	emptyLen := width - filledLen

	filledStyle := lipgloss.NewStyle().Foreground(colorAccent)
	if percent > 85 {
		filledStyle = lipgloss.NewStyle().Foreground(colorDanger)
	} else if percent > 65 {
		filledStyle = lipgloss.NewStyle().Foreground(colorWarning)
	}

	emptyStyle := lipgloss.NewStyle().Foreground(colorMuted)

	filled := filledStyle.Render(strings.Repeat("█", filledLen))
	empty := emptyStyle.Render(strings.Repeat("░", emptyLen))

	return fmt.Sprintf("[%s%s] %5.1f%%", filled, empty, percent)
}

// Sparkline renders an ASCII sparkline from a series of float values
func RenderSparkline(values []float64, maxVal float64, width int) string {
	if len(values) == 0 || width <= 0 {
		return ""
	}

	// Truncate to the most recent 'width' values
	if len(values) > width {
		values = values[len(values)-width:]
	}

	blocks := []rune{' ', ' ', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	if maxVal <= 0 {
		maxVal = 100.0
	}

	var sb strings.Builder
	for _, v := range values {
		if v < 0 {
			v = 0
		}
		if v > maxVal {
			v = maxVal
		}
		idx := int((v / maxVal) * float64(len(blocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(blocks) {
			idx = len(blocks) - 1
		}
		sb.WriteRune(blocks[idx])
	}
	return sb.String()
}

// FormatBytes formats byte counts into human-readable strings (B, KB, MB, GB)
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
