//go:build e2e

package e2e

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Jam voice answers on its published port the way an app's first packets find
// it: a listener seated in a Jam joins voice with an offer, then sends the
// server's ICE agent the connectivity check that follows, to the port on this
// host, over TCP (RFC 4571 framing) and over UDP. Only a check signed with the
// answer's credentials gets a success back, so one is the server itself
// answering through what the installer published.
func (c *client) expectVoiceAnswers(email, password, port string) {
	c.t.Helper()
	for network, err := range c.voiceAnswers(email, password, port) {
		if err != nil {
			c.t.Fatalf("Jam voice over %s on port %s: %v", network, port, err)
		}
	}
}

// voiceAnswers seats email in a Jam, joins its voice and sends the check over
// TCP and over UDP: what each one met (nil: the server answered).
func (c *client) voiceAnswers(email, password, port string) map[string]error {
	c.t.Helper()
	user, token := c.appUser(email, password)
	// A Jam of one, made as the datastore's superuser: voice asks only that
	// the listener is one of its members.
	jam := c.create("jams", map[string]any{})
	c.create("jam_members", map[string]any{"jam": jam, "user": user})

	ufrag, pwd := iceToken(8), iceToken(24)
	var joined struct {
		Session string `json:"session"`
		Answer  string `json:"answer"`
		Port    int    `json:"port"`
	}
	c.post("/api/songstress/voice/"+jam+"/join", token,
		map[string]any{"offer": voiceOffer(ufrag, pwd), "device": "cli-e2e", "reach": c.base}, &joined)
	if strconv.Itoa(joined.Port) != port {
		c.t.Fatalf("voice listens on %d, the install publishes %s", joined.Port, port)
	}
	serverUfrag, serverPwd := sdpValue(joined.Answer, "ice-ufrag"), sdpValue(joined.Answer, "ice-pwd")
	if serverUfrag == "" || serverPwd == "" {
		c.t.Fatalf("the voice answer carries no ICE credentials:\n%s", joined.Answer)
	}
	met := map[string]error{}
	for _, network := range []string{"tcp", "udp"} {
		met[network] = voiceCheck(network, net.JoinHostPort("127.0.0.1", port), serverUfrag+":"+ufrag, serverPwd)
	}
	c.post("/api/songstress/voice/"+jam+"/leave", token, map[string]any{"session": joined.Session}, nil)
	return met
}

// appUser signs in to the app as email and returns the account's id and token.
func (c *client) appUser(email, password string) (id, token string) {
	c.t.Helper()
	var auth struct {
		Token  string `json:"token"`
		Record struct {
			ID string `json:"id"`
		} `json:"record"`
	}
	c.post("/api/collections/users/auth-with-password", "", map[string]any{"identity": email, "password": password}, &auth)
	if auth.Token == "" || auth.Record.ID == "" {
		c.t.Fatalf("app sign-in as %s gave no token", email)
	}
	return auth.Record.ID, auth.Token
}

// create adds a record to collection as the datastore superuser; its id.
func (c *client) create(collection string, fields map[string]any) string {
	c.t.Helper()
	var rec struct {
		ID string `json:"id"`
	}
	c.post("/api/collections/"+collection+"/records", c.token, fields, &rec)
	return rec.ID
}

// post sends body as JSON with token (none when empty) and decodes a 2xx
// answer into out (nil: ignored).
func (c *client) post(path, token string, body, out any) {
	c.t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		c.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, c.base+path, bytes.NewReader(b))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		c.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		c.t.Fatalf("POST %s: HTTP %d %s", path, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("POST %s: %v (%s)", path, err, raw)
		}
	}
}

// voiceOffer is the least a WebRTC offer for voice holds: one Opus m-line that
// sends (the microphone), ICE credentials, and a DTLS fingerprint (never
// checked here: nothing gets as far as DTLS).
func voiceOffer(ufrag, pwd string) string {
	fingerprint := make([]string, 32)
	for i := range fingerprint {
		fingerprint[i] = fmt.Sprintf("%02X", randomBytes(1)[0])
	}
	return strings.Join([]string{
		"v=0",
		"o=- 4611731400430051336 2 IN IP4 127.0.0.1",
		"s=-",
		"t=0 0",
		"a=group:BUNDLE 0",
		"m=audio 9 UDP/TLS/RTP/SAVPF 111",
		"c=IN IP4 0.0.0.0",
		"a=ice-ufrag:" + ufrag,
		"a=ice-pwd:" + pwd,
		"a=fingerprint:sha-256 " + strings.Join(fingerprint, ":"),
		"a=setup:actpass",
		"a=mid:0",
		"a=sendonly",
		"a=rtcp-mux",
		"a=rtpmap:111 opus/48000/2",
		"a=fmtp:111 minptime=10;useinbandfec=1",
		"",
	}, "\r\n")
}

// sdpValue is the first `a=<key>:` attribute's value in an SDP.
func sdpValue(sdp, key string) string {
	for _, line := range strings.Split(sdp, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "a="+key+":"); ok {
			return v
		}
	}
	return ""
}

// voiceCheck sends one ICE connectivity check (a STUN Binding request signed
// with password, RFC 8445 7.2) to address and waits for its success answer.
// UDP may lose a packet, so it asks a few times.
func voiceCheck(network, address, username, password string) error {
	conn, err := net.DialTimeout(network, address, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	var last error
	for range 5 {
		request, txid := bindingRequest(username, password)
		if network == "tcp" { // RFC 4571: each message after its length
			request = append(binary.BigEndian.AppendUint16(nil, uint16(len(request))), request...)
		}
		if _, err := conn.Write(request); err != nil {
			return err
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if last = readBindingSuccess(conn, network, txid); last == nil || network == "tcp" {
			return last
		}
	}
	return last
}

const (
	stunCookie       = 0x2112A442
	bindingMethod    = 0x0001
	bindingSucceeded = 0x0101
	bindingFailed    = 0x0111
)

// bindingRequest is a Binding request from the controlling agent, the app,
// with USERNAME, PRIORITY, ICE-CONTROLLING, USE-CANDIDATE, then
// MESSAGE-INTEGRITY and FINGERPRINT over everything before them (RFC 8489
// 14.5 and 14.7).
func bindingRequest(username, password string) ([]byte, []byte) {
	txid := randomBytes(12)
	var attrs []byte
	add := func(kind uint16, value []byte) {
		attrs = binary.BigEndian.AppendUint16(attrs, kind)
		attrs = binary.BigEndian.AppendUint16(attrs, uint16(len(value)))
		attrs = append(attrs, value...)
		for len(attrs)%4 != 0 {
			attrs = append(attrs, 0)
		}
	}
	header := func(more int) []byte {
		h := binary.BigEndian.AppendUint16(nil, bindingMethod)
		h = binary.BigEndian.AppendUint16(h, uint16(len(attrs)+more))
		h = binary.BigEndian.AppendUint32(h, stunCookie)
		return append(h, txid...)
	}
	add(0x0006, []byte(username))                            // USERNAME
	add(0x0024, binary.BigEndian.AppendUint32(nil, 1<<24-1)) // PRIORITY
	add(0x802A, randomBytes(8))                              // ICE-CONTROLLING
	add(0x0025, nil)                                         // USE-CANDIDATE
	mac := hmac.New(sha1.New, []byte(password))
	mac.Write(append(header(24), attrs...))
	add(0x0008, mac.Sum(nil)) // MESSAGE-INTEGRITY
	fingerprint := crc32.ChecksumIEEE(append(header(8), attrs...)) ^ 0x5354554E
	add(0x8028, binary.BigEndian.AppendUint32(nil, fingerprint)) // FINGERPRINT
	return append(header(0), attrs...), txid
}

// readBindingSuccess reads the answer to the request with txid: nil for a
// success, the server's error code for a failure.
func readBindingSuccess(conn net.Conn, network string, txid []byte) error {
	var msg []byte
	if network == "tcp" {
		size := make([]byte, 2)
		if _, err := io.ReadFull(conn, size); err != nil {
			return fmt.Errorf("no answer: %w", err)
		}
		msg = make([]byte, binary.BigEndian.Uint16(size))
		if _, err := io.ReadFull(conn, msg); err != nil {
			return fmt.Errorf("a short answer: %w", err)
		}
	} else {
		buf := make([]byte, 1500)
		n, err := conn.Read(buf)
		if err != nil {
			return fmt.Errorf("no answer: %w", err)
		}
		msg = buf[:n]
	}
	if len(msg) < 20 || binary.BigEndian.Uint32(msg[4:]) != stunCookie || !bytes.Equal(msg[8:20], txid) {
		return errors.New("an answer that is not this check's")
	}
	switch kind := binary.BigEndian.Uint16(msg); kind {
	case bindingSucceeded:
		return nil
	case bindingFailed:
		return fmt.Errorf("the check was refused (STUN error response %x)", msg[20:])
	default:
		return fmt.Errorf("an answer of type %#04x", kind)
	}
}

// iceToken is n characters an ICE username fragment or password may hold.
func iceToken(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := randomBytes(n)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
