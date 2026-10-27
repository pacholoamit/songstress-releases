package deploy

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAbsPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"~/Music":       filepath.Join(home, "Music"),
		"~":             home,
		" /srv/music ":  "/srv/music",
		"./music":       filepath.Join(cwd, "music"),
		"music/../lib":  filepath.Join(cwd, "lib"),
		"/srv/music/":   "/srv/music",
		"~someone/else": filepath.Join(cwd, "~someone/else"),
		"":              "",
	}
	for in, want := range cases {
		got, err := AbsPath(in)
		if err != nil || got != want {
			t.Errorf("AbsPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
