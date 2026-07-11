package deploy

import (
	"os"
	"path/filepath"
	"strings"
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

// TestLockPersistsSMTPButNoSecrets pins the lock-file invariant: the non-secret
// SMTP fields round-trip (so re-runs remember them), and no secret sentinel can
// ride along — the SMTP password lives in Secrets, which the lock never carries.
func TestLockPersistsSMTPButNoSecrets(t *testing.T) {
	dir := t.TempDir()
	in := Lock{CLIVersion: "v0.21.0", CreatedAt: "2026-07-11T00:00:00Z", Platform: "darwin/arm64",
		Answers: Answers{Port: 8090, SkipAdminSeed: true,
			SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPUsername: "postmaster@example.com",
			SMTPFrom: "no-reply@example.com", SMTPTo: "ops@example.com", SMTPStartTLS: true},
		Pins: map[string]string{"songstress": "x"}}
	if err := WriteLock(dir, in); err != nil {
		t.Fatal(err)
	}
	out, ok, err := ReadLock(dir)
	if err != nil || !ok {
		t.Fatalf("read failed ok=%v err=%v", ok, err)
	}
	if out.Answers.SMTPHost != "smtp.example.com" || out.Answers.SMTPPort != 587 ||
		out.Answers.SMTPTo != "ops@example.com" || !out.Answers.SMTPStartTLS || !out.Answers.SkipAdminSeed {
		t.Fatalf("smtp/skip fields did not round-trip: %+v", out.Answers)
	}
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range sentinelSecrets {
		if strings.Contains(string(b), secret) {
			t.Fatalf("lock leaked secret %s", secret)
		}
	}
}
