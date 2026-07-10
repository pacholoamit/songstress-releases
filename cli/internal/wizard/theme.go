package wizard

import (
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// themed is huh's Charm theme tinted to the Songstress red ramp. huh calls it
// with the terminal's detected dark/light mode.
func themed() huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		s := huh.ThemeCharm(isDark)
		accent := lipgloss.Color("#F43648")
		s.Focused.Title = s.Focused.Title.Foreground(accent)
		s.Focused.SelectedOption = s.Focused.SelectedOption.Foreground(accent)
		s.Focused.FocusedButton = s.Focused.FocusedButton.Background(accent)
		return s
	})
}
