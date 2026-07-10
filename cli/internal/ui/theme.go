// Package ui holds the Songstress look: a red-gradient wordmark and the
// shared lipgloss styles every command renders with.
package ui

import (
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"golang.org/x/term"
)

// Songstress brand ramp — matches the app icon's red gradient.
var ramp = []string{"#FF5F6D", "#FC4A5B", "#F43648", "#E62237", "#D40E26"}

type StyleSet struct {
	Title, Ok, Warn, Err, Dim lipgloss.Style
}

var Styles = StyleSet{
	Title: lipgloss.NewStyle().Bold(true),
	Ok:    lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e")),
	Warn:  lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b")),
	Err:   lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Bold(true),
	Dim:   lipgloss.NewStyle().Faint(true),
}

// Banner renders the gradient wordmark. Plain text when not interactive.
func Banner() string {
	const word = "♪  S O N G S T R E S S"
	if !IsInteractive() {
		return word + "\n"
	}
	var b strings.Builder
	runes := []rune(word)
	for i, r := range runes {
		c := ramp[i*len(ramp)/len(runes)]
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(c)).Render(string(r)))
	}
	b.WriteString("\n" + Styles.Dim.Render(strings.Repeat("─", 28)) + "\n")
	return b.String()
}

// IsInteractive reports whether we should render color/animation: a real
// terminal on stdout and no NO_COLOR override.
func IsInteractive() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}
