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
// worker_status, and the /api/songstress/instance route.
type fakeServer struct {
	heartbeat time.Time
	instance  string // JSON body for /api/songstress/instance; "" = 404
	asked     int    // how often /api/songstress/instance was asked
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
		case path == "/api/songstress/instance" && f.instance != "":
			f.asked++
			_, _ = w.Write([]byte(f.instance))
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

var short = Budget{Health: time.Second, Worker: 300 * time.Millisecond, Music: 300 * time.Millisecond}

const ownMusicServer = `{"hasUsers":true,"musicApi":2}`

func TestHealthyStack(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now(), instance: ownMusicServer}
	if err := quick(f.start(t).URL).Run(context.Background(), short); err != nil {
		t.Fatal(err)
	}
}

func TestStaleHeartbeatFails(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now().Add(-10 * time.Minute), instance: ownMusicServer}
	err := quick(f.start(t).URL).Run(context.Background(), short)
	if err == nil || !strings.Contains(err.Error(), "worker never reported in") {
		t.Fatalf("got %v", err)
	}
}

func TestMissingHeartbeatFails(t *testing.T) {
	f := &fakeServer{instance: ownMusicServer}
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

// An image from before Songstress became its own music server answers its
// instance route without musicApi: that is final, and says what to do.
func TestAServerWithoutItsOwnMusicServerFailsFast(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now(), instance: `{"hasUsers":true}`}
	start := time.Now()
	err := quick(f.start(t).URL).Run(context.Background(), Budget{Music: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "install a newer image") {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 10*time.Second || f.asked != 1 {
		t.Fatalf("an old image can't change its mind: asked %d times in %s", f.asked, time.Since(start))
	}
}

// A server that doesn't answer its instance route yet is waited for, and the
// timeout names the route.
func TestAnUnansweredInstanceRouteTimesOut(t *testing.T) {
	f := &fakeServer{heartbeat: time.Now()}
	err := quick(f.start(t).URL).Run(context.Background(), short)
	if err == nil || !strings.Contains(err.Error(), "/api/songstress/instance: HTTP 404") {
		t.Fatalf("got %v", err)
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
