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

// Styles render plain text when color is off, so a log file or CI output
// holds no escape codes.
var Styles = newStyles(ColorEnabled())

func newStyles(color bool) StyleSet {
	if !color {
		return StyleSet{}
	}
	return StyleSet{
		Title: lipgloss.NewStyle().Bold(true),
		Ok:    lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e")),
		Warn:  lipgloss.NewStyle().Foreground(lipgloss.Color("#f59e0b")),
		Err:   lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Bold(true),
		Dim:   lipgloss.NewStyle().Faint(true),
	}
}

// Banner renders the gradient wordmark. Plain text when color is off.
func Banner() string {
	const word = "♪  S O N G S T R E S S"
	if !ColorEnabled() {
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

// Terminal reports whether stdout is a terminal (not a pipe or a file).
func Terminal() bool { return term.IsTerminal(int(os.Stdout.Fd())) }

// ColorEnabled reports whether output gets color: a terminal on stdout and
// no NO_COLOR.
func ColorEnabled() bool { return os.Getenv("NO_COLOR") == "" && Terminal() }

// CanPrompt reports whether the setup wizard can run: a terminal on stdin and
// stdout. NO_COLOR turns color off, never the wizard.
func CanPrompt() bool { return Terminal() && term.IsTerminal(int(os.Stdin.Fd())) }
