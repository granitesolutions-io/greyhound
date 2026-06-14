package ui

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// IsTTY reports whether stdout is a terminal.
var IsTTY = isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())

// Color palette — Primary can be overridden with SetPrimaryColor before any output.
var (
	Primary    = lipgloss.Color("#4285F4")
	Secondary  = lipgloss.Color("#34A853")
	Success    = lipgloss.Color("#27C93F")
	Warning    = lipgloss.Color("#F97316")
	Error      = lipgloss.Color("#EF4444")
	MutedColor = lipgloss.Color("#888888")
)

// Styles (initialized based on TTY detection).
var (
	TitleStyle     lipgloss.Style
	SuccessStyle   lipgloss.Style
	ErrorStyle     lipgloss.Style
	WarningStyle   lipgloss.Style
	InfoStyle      lipgloss.Style
	MutedStyle     lipgloss.Style
	BoldStyle      lipgloss.Style
	KeyStyle       lipgloss.Style
	ValueStyle     lipgloss.Style
	HighlightStyle lipgloss.Style
)

func init() {
	initStyles()
}

// SetPrimaryColor overrides the primary color and re-initializes styles.
// Call this before any output (e.g. before PrintHeader).
func SetPrimaryColor(c string) {
	Primary = lipgloss.Color(c)
	initStyles()
}

func initStyles() {
	if IsTTY {
		TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(Primary).
			MarginBottom(1)

		SuccessStyle = lipgloss.NewStyle().
			Foreground(Success).
			Bold(true)

		ErrorStyle = lipgloss.NewStyle().
			Foreground(Error).
			Bold(true)

		WarningStyle = lipgloss.NewStyle().
			Foreground(Warning)

		InfoStyle = lipgloss.NewStyle().
			Foreground(Secondary)

		MutedStyle = lipgloss.NewStyle().
			Foreground(MutedColor)

		BoldStyle = lipgloss.NewStyle().
			Bold(true)

		KeyStyle = lipgloss.NewStyle().
			Foreground(MutedColor)

		ValueStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E5E7EB"))

		HighlightStyle = lipgloss.NewStyle().
			Foreground(Primary).
			Bold(true)
	} else {
		plain := lipgloss.NewStyle()
		TitleStyle = plain
		SuccessStyle = plain
		ErrorStyle = plain
		WarningStyle = plain
		InfoStyle = plain
		MutedStyle = plain
		BoldStyle = plain
		KeyStyle = plain
		ValueStyle = plain
		HighlightStyle = plain
	}
}

// Timestamp returns a UTC RFC3339 timestamp string.
func Timestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// Divider returns a styled horizontal divider.
func Divider() string {
	return MutedStyle.Render("──────────────────────────────────────────────")
}

// VersionLine returns a styled version string.
func VersionLine(version string) string {
	return ValueStyle.Render(" v" + version)
}

// PrintHeader prints the startup header with banner, dividers, and version.
func PrintHeader(banner, version string) {
	fmt.Println()
	fmt.Println(Divider())
	fmt.Println(TitleStyle.Render(banner))
	fmt.Println(VersionLine(version))
	fmt.Println()
	fmt.Println(Divider())
	fmt.Println()
}

// PrintSuccess prints a success message with a green checkmark.
func PrintSuccess(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	if !IsTTY {
		fmt.Printf("%s  %s\n", Timestamp(), msg)
		return
	}
	fmt.Println(SuccessStyle.Render("\u2713 " + msg))
}

// PrintError prints an error message with a red X mark.
func PrintError(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	if !IsTTY {
		fmt.Printf("%s  ERROR %s\n", Timestamp(), msg)
		return
	}
	fmt.Println(ErrorStyle.Render("\u2717 " + msg))
}

// PrintWarning prints a warning message.
func PrintWarning(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	if !IsTTY {
		fmt.Printf("%s  WARN %s\n", Timestamp(), msg)
		return
	}
	fmt.Println(WarningStyle.Render("\u26a0 " + msg))
}

// PrintInfo prints an info message with a bullet point.
func PrintInfo(format string, a ...interface{}) {
	msg := fmt.Sprintf(format, a...)
	if !IsTTY {
		fmt.Printf("%s  %s\n", Timestamp(), msg)
		return
	}
	fmt.Println(InfoStyle.Render("\u2022 " + msg))
}

// PrintKeyValue prints a formatted key-value pair.
func PrintKeyValue(key, value string) {
	if !IsTTY {
		fmt.Printf("%s  %s: %s\n", Timestamp(), key, value)
		return
	}
	fmt.Printf("%s: %s\n", KeyStyle.Render(key), ValueStyle.Render(value))
}

// Highlight returns text styled with the primary color and bold.
func Highlight(s string) string {
	return HighlightStyle.Render(s)
}

// Muted returns text in the muted/gray color.
func Muted(s string) string {
	return MutedStyle.Render(s)
}

// Bold returns bold text.
func Bold(s string) string {
	return BoldStyle.Render(s)
}
