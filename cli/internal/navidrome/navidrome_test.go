package navidrome

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type user struct {
	pass  string
	admin bool
}

// fakeNavidrome answers like Navidrome 0.62: a bad login is Subsonic error 40
// (HTTP 200); getUser reports adminRole. It fails the test if a request ever
// carries a password in plain text.
func fakeNavidrome(t *testing.T, users map[string]user) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		for _, u := range users {
			if q.Get("p") == u.pass || strings.Contains(string(body), u.pass) {
				t.Errorf("plaintext password sent to %s", r.URL.Path)
			}
		}
		u, ok := users[q.Get("u")]
		sum := md5.Sum([]byte(u.pass + q.Get("s")))
		if !ok || hex.EncodeToString(sum[:]) != q.Get("t") {
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"failed","error":{"code":40,"message":"Wrong username or password"}}}`))
			return
		}
		switch r.URL.Path {
		case "/rest/ping":
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","version":"1.16.1"}}`))
		case "/rest/getUser":
			admin := "false"
			if u.admin {
				admin = "true"
			}
			_, _ = w.Write([]byte(`{"subsonic-response":{"status":"ok","user":{"username":"` + q.Get("u") + `","adminRole":` + admin + `}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheck(t *testing.T) {
	nd := fakeNavidrome(t, map[string]user{"admin": {"right-pass", true}, "bob": {"bob-pass", false}})
	cases := []struct {
		name, user, pass, reason string
	}{
		{"admin signs in", "admin", "right-pass", ""},
		{"wrong password", "admin", "wrong-pass", Auth},
		{"unknown user", "nobody", "some-pass", Auth},
		{"non-admin refused", "bob", "bob-pass", NotAdmin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Check(context.Background(), nil, nd.URL+"/", c.user, c.pass)
			if got := Reason(err); got != c.reason {
				t.Fatalf("reason = %q (err %v), want %q", got, err, c.reason)
			}
		})
	}
}

func TestCheckUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // nothing listens there now
	err := Check(context.Background(), nil, base, "admin", "x")
	if Reason(err) != Unreachable || !strings.Contains(err.Error(), "can't reach") {
		t.Fatalf("got %v", err)
	}
}

func TestCheckNotNavidrome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>hello</html>"))
	}))
	defer srv.Close()
	err := Check(context.Background(), nil, srv.URL, "admin", "x")
	if Reason(err) != NotNavidrome {
		t.Fatalf("got %v", err)
	}
}
