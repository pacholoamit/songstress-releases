package verify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeServer is a just-enough Songstress: PocketBase health + superuser auth +
// worker_status + connections + jobs, and the /api/songstress/health route.
type fakeServer struct {
	heartbeat  time.Time
	health     string // JSON body for /api/songstress/health; "" = 404 (pre-v0.32 server)
	spaHealth  bool   // answer /api/songstress/health with the web app's index.html (v0.31)
	connStatus string
	connError  string
	jobStatus  string
	jobError   string
	jobsMade   int
}

func (f *fakeServer) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authed := r.Header.Get("Authorization") == "su-token"
		path := r.URL.Path
		switch {
		case path == "/api/health":
			w.WriteHeader(http.StatusOK)
		case path == "/api/collections/_superusers/auth-with-password":
			var c struct{ Identity, Password string }
			_ = json.NewDecoder(r.Body).Decode(&c)
			if c.Password != "su-pass" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"token":"su-token"}`))
		case !authed && strings.HasPrefix(path, "/api/collections/"):
			w.WriteHeader(http.StatusUnauthorized)
		case path == "/api/collections/worker_status/records":
			items := []map[string]string{}
			if !f.heartbeat.IsZero() {
				items = append(items, map[string]string{"last_seen": f.heartbeat.UTC().Format("2006-01-02 15:04:05.000Z")})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
		case path == "/api/collections/connections/records":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]string{{"status": f.connStatus, "last_error": f.connError}}})
		case path == "/api/collections/jobs/records" && r.Method == http.MethodPost:
			f.jobsMade++
			_, _ = w.Write([]byte(`{"id":"job1"}`))
		case path == "/api/collections/jobs/records/job1":
			_ = json.NewEncoder(w).Encode(map[string]string{"status": f.jobStatus, "error": f.jobError})
		case path == "/api/songstress/health" && f.spaHealth:
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<!doctype html><title>Songstress</title>"))
		case path == "/api/songstress/health" && f.health != "":
			_, _ = w.Write([]byte(f.health))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func quick(base string) Stack {
	return Stack{Base: base, Email: "admin@x", Password: "su-pass", Every: 10 * time.Millisecond}
}

var short = Budget{Health: time.Second, Worker: 300 * time.Millisecond, Navidrome: 300 * time.Millisecond}

func TestHealthyStack(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now(), health: `{"ok":true,"navidrome":true}`}
	if err := quick(f.start(t).URL).Run(context.Background(), short); err != nil {
		t.Fatal(err)
	}
}

func TestStaleHeartbeatFails(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now().Add(-10 * time.Minute), health: `{"ok":true,"navidrome":true}`}
	err := quick(f.start(t).URL).Run(context.Background(), short)
	if err == nil || !strings.Contains(err.Error(), "worker never reported in") {
		t.Fatalf("got %v", err)
	}
}

func TestMissingHeartbeatFails(t *testing.T) {
	f := &fakeServer{health: `{"ok":true,"navidrome":true}`}
	err := quick(f.start(t).URL).Run(context.Background(), short)
	if err == nil || !strings.Contains(err.Error(), "no heartbeat yet") {
		t.Fatalf("got %v", err)
	}
}

func TestWrongSuperuserPasswordIsExplained(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now()}
	s := quick(f.start(t).URL)
	s.Password = "nope"
	err := s.Run(context.Background(), short)
	if err == nil || !strings.Contains(err.Error(), "SONGSTRESS_ADMIN_PASSWORD") {
		t.Fatalf("got %v", err)
	}
}

// A rejected Navidrome login that the worker's boot check also failed on is
// final — the installer stops waiting and prints the worker's explanation.
func TestRejectedNavidromeLoginFailsFastWithTheWorkersExplanation(t *testing.T) {
	f := &fakeServer{
		heartbeat:  time.Now(),
		health:     `{"ok":true,"navidrome":false,"navidrome_reason":"auth"}`,
		connStatus: "error",
		connError:  "Navidrome rejected the admin password Songstress has saved.",
	}
	start := time.Now()
	err := quick(f.start(t).URL).Run(context.Background(), Budget{Navidrome: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "rejected the admin password") {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("a confirmed rejected login should not wait out the budget")
	}
}

func TestNavidromeNotCheckedYetTimesOut(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now(), health: `{"ok":true,"navidrome":null}`}
	err := quick(f.start(t).URL).Run(context.Background(), short)
	if err == nil || !strings.Contains(err.Error(), "hasn't checked Navidrome yet") {
		t.Fatalf("got %v", err)
	}
}

// v0.31 and older have no /api/songstress/health: the worker's own
// test_connection job is the sign-in proof instead.
func TestLegacyServerUsesATestConnectionJob(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now(), jobStatus: "succeeded"}
	if err := quick(f.start(t).URL).Run(context.Background(), short); err != nil {
		t.Fatal(err)
	}
	if f.jobsMade != 1 {
		t.Fatalf("jobs created = %d", f.jobsMade)
	}

	f = &fakeServer{heartbeat: time.Now(), jobStatus: "failed", jobError: "navidrome: Wrong username or password"}
	err := quick(f.start(t).URL).Run(context.Background(), Budget{Navidrome: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "Wrong username or password") {
		t.Fatalf("got %v", err)
	}

	// v0.31 serves its web app's index.html for unknown paths — still legacy.
	f = &fakeServer{heartbeat: time.Now(), spaHealth: true, jobStatus: "succeeded"}
	if err := quick(f.start(t).URL).Run(context.Background(), short); err != nil || f.jobsMade != 1 {
		t.Fatalf("an HTML answer must fall back to the job: err=%v jobs=%d", err, f.jobsMade)
	}
}

func TestParseTime(t *testing.T) {
	for _, v := range []string{"2026-10-22 00:52:25.316Z", "2026-10-22T00:52:25Z", "2026-10-22 00:52:25Z"} {
		if _, err := parseTime(v); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	if _, err := parseTime("yesterday"); err == nil {
		t.Error("expected an error")
	}
}
