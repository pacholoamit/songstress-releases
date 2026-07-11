package wizard

import "testing"

// TestDeriveSMTPTo pins the post-form recipient derivation the wizard relies on
// (huh's Value() snapshots at build time, so SMTP_TO can't be prefilled from the
// admin email typed during the same form — it must be resolved afterwards).
func TestDeriveSMTPTo(t *testing.T) {
	cases := []struct {
		name       string
		entered    string
		adminEmail string
		skipAdmin  bool
		want       string
	}{
		{"blank inherits typed admin email (generate/choose)", "", "op@example.com", false, "op@example.com"},
		{"blank stays blank in skip mode", "", "op@example.com", true, ""},
		{"explicit recipient wins over admin email", "ops@example.com", "op@example.com", false, "ops@example.com"},
		{"explicit recipient wins even in skip mode", "ops@example.com", "op@example.com", true, "ops@example.com"},
		{"no admin email and no entry stays blank", "", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveSMTPTo(c.entered, c.adminEmail, c.skipAdmin); got != c.want {
				t.Fatalf("deriveSMTPTo(%q, %q, %v) = %q, want %q", c.entered, c.adminEmail, c.skipAdmin, got, c.want)
			}
		})
	}
}
