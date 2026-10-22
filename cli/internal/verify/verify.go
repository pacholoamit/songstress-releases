// Package verify decides whether a fresh or re-run install actually works, the
// way a person using it would notice: the server answers, the worker is alive
// (its heartbeat is fresh), and Songstress can sign in to Navidrome. A bare
// /api/health said "up" for installs whose Navidrome login was wrong, or whose
// worker never got past its boot.
package verify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Stack is one running Songstress, reached on Base (e.g. http://127.0.0.1:8090)
// as its datastore superuser.
type Stack struct {
	Base            string
	Email, Password string
	Client          *http.Client
	// Every is the polling interval (default 2s).
	Every time.Duration
	// Say reports progress lines; nil is silent.
	Say func(string)
}

// Budget bounds each wait. Zero values take the defaults.
type Budget struct {
	Health, Worker, Navidrome time.Duration
}

func (b Budget) or() Budget {
	def := func(d, v time.Duration) time.Duration {
		if d == 0 {
			return v
		}
		return d
	}
	return Budget{def(b.Health, 2*time.Minute), def(b.Worker, 3*time.Minute), def(b.Navidrome, 3*time.Minute)}
}

// HeartbeatMaxAge is how old the worker's last heartbeat may be (it beats every
// ~15s; the server's own watchdog calls 60s "down").
const HeartbeatMaxAge = 60 * time.Second

// Run checks, in order: /api/health, a fresh worker heartbeat, and Navidrome.
func (s Stack) Run(ctx context.Context, b Budget) error {
	b = b.or()
	if s.Client == nil {
		s.Client = &http.Client{Timeout: 10 * time.Second}
	}
	if s.Every == 0 {
		s.Every = 2 * time.Second
	}
	s.say("Waiting for the server…")
	if err := s.until(ctx, b.Health, s.healthy); err != nil {
		return fmt.Errorf("the server never answered /api/health: %w", err)
	}
	token, err := s.superuser(ctx)
	if err != nil {
		return err
	}
	s.say("Waiting for the worker's first heartbeat…")
	if err := s.until(ctx, b.Worker, func(ctx context.Context) error { return s.workerFresh(ctx, token) }); err != nil {
		return fmt.Errorf("the worker never reported in: %w", err)
	}
	s.say("Checking Songstress can sign in to Navidrome…")
	return s.navidrome(ctx, token, b.Navidrome)
}

func (s Stack) say(line string) {
	if s.Say != nil {
		s.Say(line)
	}
}

// until retries check every s.Every until it returns nil or `limit` passes;
// the last error explains the timeout.
func (s Stack) until(ctx context.Context, limit time.Duration, check func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var last error
	for {
		err := check(ctx)
		if err == nil {
			return nil
		}
		var final *finalError
		if errors.As(err, &final) {
			return final.err
		}
		// A request cut off by the deadline says nothing new; report the last
		// real answer instead ("no heartbeat yet", not "context deadline").
		if ctx.Err() == nil || last == nil {
			last = err
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(s.Every):
		}
	}
}

// finalError stops `until` early: retrying can't change the answer.
type finalError struct{ err error }

func (f *finalError) Error() string { return f.err.Error() }

func (s Stack) healthy(ctx context.Context) error {
	code, _, err := s.do(ctx, http.MethodGet, "/api/health", "", nil)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("HTTP %d", code)
	}
	return nil
}

func (s Stack) superuser(ctx context.Context) (string, error) {
	body := map[string]string{"identity": s.Email, "password": s.Password}
	code, raw, err := s.do(ctx, http.MethodPost, "/api/collections/_superusers/auth-with-password", "", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &out) != nil || out.Token == "" {
		return "", fmt.Errorf("couldn't sign in as the datastore superuser %s (HTTP %d) — is SONGSTRESS_ADMIN_PASSWORD in .env the one the server was first started with?", s.Email, code)
	}
	return out.Token, nil
}

func (s Stack) workerFresh(ctx context.Context, token string) error {
	q := url.Values{"sort": {"created"}, "perPage": {"1"}}
	code, raw, err := s.do(ctx, http.MethodGet, "/api/collections/worker_status/records?"+q.Encode(), token, nil)
	if err != nil {
		return err
	}
	var list struct {
		Items []struct {
			LastSeen string `json:"last_seen"`
		} `json:"items"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &list) != nil {
		return fmt.Errorf("worker_status: HTTP %d", code)
	}
	if len(list.Items) == 0 {
		return errors.New("no heartbeat yet")
	}
	seen, err := parseTime(list.Items[0].LastSeen)
	if err != nil {
		return err
	}
	if age := time.Since(seen); age > HeartbeatMaxAge {
		return fmt.Errorf("last heartbeat %s ago", age.Round(time.Second))
	}
	return nil
}

// parseTime reads PocketBase datetimes ("2026-10-22 00:52:25.316Z") and RFC 3339.
func parseTime(v string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05.999Z07:00", time.RFC3339Nano} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unreadable heartbeat time %q", v)
}

// navidrome waits for /api/songstress/health to report navidrome:true. Servers
// from before that route (v0.31 and older) answer it with a 404 or with the web
// app's index.html; they get the same proof from a test_connection job instead:
// an authenticated sign-in run by the worker.
func (s Stack) navidrome(ctx context.Context, token string, limit time.Duration) error {
	legacy := false
	err := s.until(ctx, limit, func(ctx context.Context) error {
		code, raw, err := s.do(ctx, http.MethodGet, "/api/songstress/health", "", nil)
		if err != nil {
			return err
		}
		var h struct {
			OK        *bool  `json:"ok"`
			Navidrome *bool  `json:"navidrome"`
			Reason    string `json:"navidrome_reason"`
		}
		if code == http.StatusNotFound || (code == http.StatusOK && (json.Unmarshal(raw, &h) != nil || h.OK == nil)) {
			legacy = true
			return nil
		}
		if code != http.StatusOK {
			return fmt.Errorf("/api/songstress/health: HTTP %d", code)
		}
		switch {
		case h.Navidrome == nil:
			return errors.New("the server hasn't checked Navidrome yet")
		case !*h.Navidrome:
			failed := fmt.Errorf("Navidrome check failed (%s)", h.Reason)
			// A rejected login the worker's boot check also failed on doesn't
			// fix itself — stop waiting. (Its boot check may still heal from a
			// corrected .env, so "auth" alone isn't final.)
			if status, _ := s.connection(ctx, token); h.Reason == "auth" && status == "error" {
				return &finalError{failed}
			}
			return failed
		}
		return nil
	})
	if err == nil && legacy {
		return s.testConnectionJob(ctx, token, limit)
	}
	if err != nil {
		hint := ""
		if _, lastError := s.connection(ctx, token); lastError != "" {
			hint = "\n  " + lastError
		}
		return fmt.Errorf("Songstress can't use Navidrome: %w%s", err, hint)
	}
	return nil
}

// connection reads the Navidrome connection's status and the worker's own
// plain-language last_error ("", "" when unavailable).
func (s Stack) connection(ctx context.Context, token string) (status, lastError string) {
	q := url.Values{"filter": {`type = "navidrome"`}, "perPage": {"1"}}
	code, raw, err := s.do(ctx, http.MethodGet, "/api/collections/connections/records?"+q.Encode(), token, nil)
	if err != nil || code != http.StatusOK {
		return "", ""
	}
	var list struct {
		Items []struct {
			Status    string `json:"status"`
			LastError string `json:"last_error"`
		} `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil || len(list.Items) == 0 {
		return "", ""
	}
	return list.Items[0].Status, list.Items[0].LastError
}

func (s Stack) testConnectionJob(ctx context.Context, token string, limit time.Duration) error {
	job := map[string]any{
		"type": "test_connection", "status": "queued", "requested_by": "system",
		"payload": map[string]string{"type": "navidrome"},
	}
	code, raw, err := s.do(ctx, http.MethodPost, "/api/collections/jobs/records", token, job)
	if err != nil {
		return err
	}
	var created struct {
		ID string `json:"id"`
	}
	if code != http.StatusOK || json.Unmarshal(raw, &created) != nil || created.ID == "" {
		return fmt.Errorf("couldn't queue a Navidrome check (HTTP %d): %s", code, bytes.TrimSpace(raw))
	}
	return s.until(ctx, limit, func(ctx context.Context) error {
		code, raw, err := s.do(ctx, http.MethodGet, "/api/collections/jobs/records/"+created.ID, token, nil)
		if err != nil {
			return err
		}
		var j struct {
			Status, Error string
		}
		if code != http.StatusOK || json.Unmarshal(raw, &j) != nil {
			return fmt.Errorf("job lookup: HTTP %d", code)
		}
		switch j.Status {
		case "succeeded":
			return nil
		case "failed", "cancelled", "skipped":
			return &finalError{fmt.Errorf("Songstress can't use Navidrome: %s", strings.TrimSpace(j.Error))}
		}
		return fmt.Errorf("Navidrome check still %s", j.Status)
	})
}

func (s Stack) do(ctx context.Context, method, path, token string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.Base, "/")+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, err
}
