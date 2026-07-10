package deploy

import (
	"path/filepath"
	"testing"
)

func TestLockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Lock{CLIVersion: "v0.21.0", CreatedAt: "2026-07-11T00:00:00Z", Platform: "darwin/arm64",
		Answers: Answers{Port: 8090}, Pins: map[string]string{"songstress": "x"}}
	if err := WriteLock(dir, in); err != nil {
		t.Fatal(err)
	}
	out, ok, err := ReadLock(dir)
	if err != nil || !ok {
		t.Fatalf("read failed ok=%v err=%v", ok, err)
	}
	if out.CLIVersion != in.CLIVersion || out.Pins["songstress"] != "x" || out.Answers.Port != 8090 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	if _, ok, _ := ReadLock(filepath.Join(dir, "nope")); ok {
		t.Fatal("expected absent lock")
	}
}
