// Package navidrome checks an existing Navidrome before the installer points
// Songstress at it: the server answers, the login signs in (an AUTHENTICATED
// Subsonic ping — Navidrome's bare /ping answers anyone), and the account is a
// Navidrome admin, which Songstress needs to start library scans and to create
// accounts for the people the operator invites. Every call uses Subsonic's
// salted-token auth, so the password itself never crosses the wire.
package navidrome

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Reasons a check can fail. They pick the guidance the installer prints.
const (
	Unreachable  = "unreachable"
	Auth         = "auth"
	NotAdmin     = "not-admin"
	NotNavidrome = "not-navidrome"
)

// CheckError is a failed check with a plain-language explanation.
type CheckError struct {
	Reason string
	Msg    string
}

func (e *CheckError) Error() string { return e.Msg }

// Check verifies base/user/pass against a live Navidrome. A nil client uses a
// 10s-timeout default.
func Check(ctx context.Context, client *http.Client, base, user, pass string) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	base = strings.TrimRight(base, "/")
	if _, err := subsonic(ctx, client, base, user, pass, "ping", nil); err != nil {
		return err
	}
	// getUser on itself reports adminRole — same salted-token auth as the ping
	// (native /auth/login would need the password in plain text).
	who, err := subsonic(ctx, client, base, user, pass, "getUser", url.Values{"username": {user}})
	if err != nil {
		return err
	}
	if !who.Response.User.AdminRole {
		return &CheckError{NotAdmin, fmt.Sprintf(
			"Navidrome account %q is not an admin — Songstress needs an admin account to scan your library and create accounts for the people you invite", user)}
	}
	return nil
}

type subsonicReply struct {
	Response struct {
		Status string `json:"status"`
		Error  struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		User struct {
			AdminRole bool `json:"adminRole"`
		} `json:"user"`
	} `json:"subsonic-response"`
}

// subsonic calls one Subsonic view with salted-token auth and classifies any
// failure into a CheckError.
func subsonic(ctx context.Context, client *http.Client, base, user, pass, view string, extra url.Values) (subsonicReply, error) {
	var reply subsonicReply
	salt := randomHex(8)
	sum := md5.Sum([]byte(pass + salt))
	q := url.Values{
		"u": {user}, "t": {hex.EncodeToString(sum[:])}, "s": {salt},
		"v": {"1.16.1"}, "c": {"songstress-cli"}, "f": {"json"},
	}
	for k, v := range extra {
		q[k] = v
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/rest/"+view+"?"+q.Encode(), nil)
	if err != nil {
		return reply, &CheckError{NotNavidrome, fmt.Sprintf("%q is not a usable URL: %v", base, err)}
	}
	resp, err := client.Do(req)
	if err != nil {
		return reply, &CheckError{Unreachable, fmt.Sprintf("can't reach Navidrome at %s: %v", base, err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return reply, &CheckError{Unreachable, fmt.Sprintf("Navidrome at %s answered HTTP %d", base, resp.StatusCode)}
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&reply) != nil || reply.Response.Status == "" {
		return reply, &CheckError{NotNavidrome, fmt.Sprintf("%s did not answer like a Navidrome/Subsonic server (HTTP %d) — check the URL", base, resp.StatusCode)}
	}
	switch {
	case reply.Response.Status == "ok":
		return reply, nil
	case reply.Response.Error.Code == 40 || reply.Response.Error.Code == 41:
		return reply, &CheckError{Auth, fmt.Sprintf("Navidrome at %s rejected the username or password for %q", base, user)}
	default:
		return reply, &CheckError{NotNavidrome, fmt.Sprintf("Navidrome at %s refused the check: %s", base, reply.Response.Error.Message)}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return hex.EncodeToString(b)
}

// Reason extracts a CheckError's reason ("" for any other error).
func Reason(err error) string {
	var ce *CheckError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}
