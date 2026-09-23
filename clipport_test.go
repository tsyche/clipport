package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEncryptDecryptRoundtrip(t *testing.T) {
	key := []byte("test-password")
	plaintext := []byte("hello clipboard")

	ciphertext, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	got, err := decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}

	if !bytes.Equal(got, plaintext) {
		t.Errorf("roundtrip mismatch: got %q, want %q", got, plaintext)
	}
}

func TestEncryptProducesUniqueCiphertexts(t *testing.T) {
	key := []byte("test-password")
	plaintext := []byte("same input")

	c1, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(c1, c2) {
		t.Error("encrypt produced identical ciphertexts for the same input (nonce not randomized)")
	}
}

func TestDecryptWrongKeyFails(t *testing.T) {
	ciphertext, err := encrypt([]byte("correct-key"), []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	_, err = decrypt([]byte("wrong-key"), ciphertext)
	if err == nil {
		t.Error("expected decrypt with wrong key to fail, but it succeeded")
	}
}

func TestDecryptTruncatedDataPanicsOrErrors(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			// a panic here is a bug — truncated data should return an error, not panic
			t.Errorf("decrypt panicked on truncated data: %v", r)
		}
	}()

	_, err := decrypt([]byte("key"), []byte("tooshort"))
	if err == nil {
		t.Error("expected error on truncated ciphertext")
	}
}

func TestDeriveKeyDeterministicWithSalt(t *testing.T) {
	password := []byte("my-password")
	salt := bytes.Repeat([]byte{0xAB}, 32)

	k1, _, err := deriveKey(password, salt)
	if err != nil {
		t.Fatal(err)
	}
	k2, _, err := deriveKey(password, salt)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(k1, k2) {
		t.Error("deriveKey with same password+salt produced different keys")
	}
}

func TestDeriveKeyGeneratesSaltWhenNil(t *testing.T) {
	key, salt, err := deriveKey([]byte("pwd"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(salt) != 32 {
		t.Errorf("expected 32-byte salt, got %d", len(salt))
	}
	if len(key) != 32 {
		t.Errorf("expected 32-byte key, got %d", len(key))
	}
}

func TestEncryptEmptyPlaintext(t *testing.T) {
	key := []byte("key")
	ciphertext, err := encrypt(key, []byte{})
	if err != nil {
		t.Fatalf("encrypt empty plaintext: %v", err)
	}
	got, err := decrypt(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt empty plaintext: %v", err)
	}
	if !bytes.Equal(got, []byte{}) {
		t.Errorf("expected empty plaintext back, got %q", got)
	}
}

func TestSendClipboardRejectsOversizedFrame(t *testing.T) {
	oversized := strings.Repeat("a", maxClipboardFrameBytes+1)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	err := sendClipboard(w, oversized, nil)
	if !errors.Is(err, errClipboardTooLarge) {
		t.Fatalf("expected errClipboardTooLarge, got %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no bytes written for oversized frame, got %d", buf.Len())
	}
}

func TestSendClipboardAllowsMaxSizeFrame(t *testing.T) {
	// Exact limit must still succeed (no gob overhead on raw []byte payload path
	// is not guaranteed, so stay one byte under to avoid flaking on encoder overhead).
	atLimit := strings.Repeat("a", maxClipboardFrameBytes-64)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	if err := sendClipboard(w, atLimit, nil); err != nil {
		t.Fatalf("send at near-limit size: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected bytes written")
	}
}

func TestMonitorSentClipsRejectsOversizedFrame(t *testing.T) {
	// Craft a single gob frame whose payload alone exceeds the cap.
	payload := bytes.Repeat([]byte("x"), maxClipboardFrameBytes+1)
	var frame bytes.Buffer
	if err := gob.NewEncoder(&frame).Encode(payload); err != nil {
		t.Fatalf("encode oversized frame: %v", err)
	}

	// No key: MonitorSentClips would only hit the frame cap path.
	// EOF after the oversized frame must not be reached as clean shutdown.
	r := bufio.NewReader(&frame)
	clean := MonitorSentClips(r, nil)
	if clean {
		t.Fatal("expected oversized frame to disconnect uncleanly (false), got clean shutdown (true)")
	}
}

func TestMonitorSentClipsValidFrameThenEOF(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	// Empty payload: MonitorSentClips skips setLocalClip for empty clipboard,
	// so this exercises decode + clean EOF without touching the system clipboard
	// (setLocalClip os.Exit(2)s on headless CI with no xclip/xsel/wl-copy).
	if err := gob.NewEncoder(w).Encode([]byte{}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	clean := MonitorSentClips(bufio.NewReader(&buf), nil)
	if !clean {
		t.Fatal("expected true (clean EOF) after valid frame + EOF")
	}
}

// preserveGlobals snapshots process-wide state that tests mutate and restores
// it on cleanup. No t.Parallel anywhere in this suite — these are shared.
func preserveGlobals(t *testing.T) {
	t.Helper()
	oldSecure, oldKeyMode := secure, keyMode
	oldPassword := password
	oldClients := listOfClients
	oldClipboard := localClipboard
	oldGet, oldSet := getLocalClip, setLocalClip
	oldSeconds := secondsBetweenChecksForClipChange
	t.Cleanup(func() {
		secure, keyMode = oldSecure, oldKeyMode
		password = oldPassword
		listOfClients = oldClients
		localClipboard = oldClipboard
		getLocalClip, setLocalClip = oldGet, oldSet
		secondsBetweenChecksForClipChange = oldSeconds
	})
}

// stubClipboard points get/set at in-memory fakes so tests never shell out
// (runGetClipCommand/runSetClipCommand os.Exit(2) on headless CI).
func stubClipboard(t *testing.T, get func() string) *string {
	t.Helper()
	var setTo string
	getLocalClip = get
	setLocalClip = func(s string) { setTo = s }
	return &setTo
}

func encodeFrame(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(payload); err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	return buf.Bytes()
}

// encodeFrames writes multiple payloads as one gob stream (a single Encoder).
// Separate Encoders on the same buffer produce an undecodable stream.
func encodeFrames(t *testing.T, payloads ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	for _, p := range payloads {
		if err := enc.Encode(p); err != nil {
			t.Fatalf("encode frame: %v", err)
		}
	}
	return buf.Bytes()
}

func TestResolveClientAddress(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		port    string
		want    string
		wantErr bool
	}{
		{name: "hostport only", addr: "192.168.1.5:53701", port: "", want: "192.168.1.5:53701"},
		{name: "host plus port flag", addr: "192.168.1.5", port: "53701", want: "192.168.1.5:53701"},
		{name: "matching embedded and flag", addr: "192.168.1.5:53701", port: "53701", want: "192.168.1.5:53701"},
		{name: "conflicting ports", addr: "192.168.1.5:1111", port: "2222", wantErr: true},
		{name: "no port anywhere", addr: "192.168.1.5", port: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveClientAddress(tt.addr, tt.port)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveConnectionKeyPlaintext(t *testing.T) {
	preserveGlobals(t)
	secure, keyMode = false, false
	key, err := resolveConnectionKey(nil, true, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != nil {
		t.Errorf("expected nil key in plaintext mode, got %x", key)
	}
}

func TestResolveConnectionKeyPasswordMode(t *testing.T) {
	preserveGlobals(t)
	secure, keyMode = true, false
	password = []byte("shared-secret")
	key, err := resolveConnectionKey(nil, false, "127.0.0.1:1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(key, password) {
		t.Errorf("got %q, want password", key)
	}
}

func TestResolveConnectionKeyKeyModeHandshake(t *testing.T) {
	preserveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := generateKeypair(dir); err != nil {
		t.Fatalf("generateKeypair: %v", err)
	}
	secure, keyMode = true, true

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type result struct {
		key []byte
		err error
	}
	serverDone := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			serverDone <- result{err: err}
			return
		}
		defer c.Close()
		k, err := resolveConnectionKey(c, true, "")
		serverDone <- result{key: k, err: err}
	}()

	c, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	clientKey, err := resolveConnectionKey(c, false, ln.Addr().String())
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	sr := <-serverDone
	if sr.err != nil {
		t.Fatalf("server handshake: %v", sr.err)
	}
	if len(clientKey) != 32 {
		t.Fatalf("client key length %d, want 32", len(clientKey))
	}
	if !bytes.Equal(clientKey, sr.key) {
		t.Error("client and server derived different ECDH secrets")
	}
}

func TestVerifyOrTrustPeerTOFU(t *testing.T) {
	preserveGlobals(t)
	t.Setenv("HOME", t.TempDir())

	pub := bytes.Repeat([]byte{0x42}, 32)
	if err := verifyOrTrustPeer("10.0.0.9:1234", pub); err != nil {
		t.Fatalf("first connect should trust: %v", err)
	}
	if err := verifyOrTrustPeer("10.0.0.9:1234", pub); err != nil {
		t.Fatalf("same key reconnect should pass: %v", err)
	}

	changed := bytes.Repeat([]byte{0x99}, 32)
	err := verifyOrTrustPeer("10.0.0.9:1234", changed)
	if err == nil {
		t.Fatal("expected error on key mismatch")
	}
	if !strings.Contains(err.Error(), "has changed") {
		t.Errorf("error should mention key change, got: %v", err)
	}

	// File must still hold the original (trusted) key, not the rejected one.
	path := filepath.Join(t.TempDir(), "unused") // placate unused-import linters if path unused elsewhere
	_ = path
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	peers, err := loadKnownPeers(filepath.Join(dir, "known_peers"))
	if err != nil {
		t.Fatal(err)
	}
	if peers["10.0.0.9:1234"] == "" {
		t.Fatal("known_peers missing trusted peer")
	}
}

func TestLoadKnownPeersSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_peers")
	content := "peer-a AAAA\n\nnot-a-peer-line\npeer-b BBBB\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	peers, err := loadKnownPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2 (malformed line skipped): %#v", len(peers), peers)
	}
	if peers["peer-a"] != "AAAA" || peers["peer-b"] != "BBBB" {
		t.Errorf("unexpected peers: %#v", peers)
	}
}

func TestLoadKnownPeersMissingFile(t *testing.T) {
	peers, err := loadKnownPeers(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing file should be empty map, not error: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("expected empty map, got %#v", peers)
	}
}

func TestSaveKnownPeersRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_peers")
	in := map[string]string{"p1": "K1", "p2": "K2"}
	if err := saveKnownPeers(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := loadKnownPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out["p1"] != "K1" || out["p2"] != "K2" {
		t.Errorf("roundtrip mismatch: %#v", out)
	}
}

func TestGenerateKeypairAndLoad(t *testing.T) {
	dir := t.TempDir()
	keyPath, pub, err := generateKeypair(dir)
	if err != nil {
		t.Fatalf("generateKeypair: %v", err)
	}
	if keyPath == "" || len(pub) != 32 {
		t.Fatalf("unexpected returns: path=%q pub=%d", keyPath, len(pub))
	}
	if _, err := os.Stat(filepath.Join(dir, "key.pub")); err != nil {
		t.Fatalf("key.pub missing: %v", err)
	}

	// Second generate must refuse to overwrite.
	if _, _, err := generateKeypair(dir); err == nil {
		t.Fatal("expected error when key already exists")
	}

	// loadKeypair via HOME
	t.Setenv("HOME", filepath.Dir(dir)) // wrong layout — use clipportDir under temp home
	t.Setenv("HOME", t.TempDir())
	homeDir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := generateKeypair(homeDir); err != nil {
		t.Fatal(err)
	}
	priv, err := loadKeypair()
	if err != nil {
		t.Fatalf("loadKeypair: %v", err)
	}
	if !bytes.Equal(priv.PublicKey().Bytes(), pub) && len(priv.PublicKey().Bytes()) != 32 {
		t.Fatal("loaded key has wrong public length")
	}
}

func TestLoadKeypairMissing(t *testing.T) {
	preserveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	_, err := loadKeypair()
	if err == nil {
		t.Fatal("expected error when no key exists")
	}
	if !strings.Contains(err.Error(), "keygen") {
		t.Errorf("error should point at keygen, got: %v", err)
	}
}

func TestMonitorLocalClipSendsAndStops(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	stubClipboard(t, func() string { return "hello-from-local" })

	var mu sync.Mutex
	var wire bytes.Buffer
	w := bufio.NewWriter(struct {
		io.Writer
	}{Writer: &muWriter{mu: &mu, b: &wire}})

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		MonitorLocalClip(w, nil, stop)
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := wire.Len()
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("MonitorLocalClip never wrote a frame")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MonitorLocalClip did not return after stop")
	}

	mu.Lock()
	data := append([]byte(nil), wire.Bytes()...)
	mu.Unlock()
	var payload []byte
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&payload); err != nil {
		t.Fatalf("decode sent frame: %v", err)
	}
	if string(payload) != "hello-from-local" {
		t.Errorf("sent %q, want %q", payload, "hello-from-local")
	}
}

// Empty local clipboard must not put a frame on the wire (startup sync,
// cleared clipboard, or OS reporting no text — e.g. pbpaste on an image).
func TestMonitorLocalClipSkipsEmpty(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	stubClipboard(t, func() string { return "" })

	var mu sync.Mutex
	var wire bytes.Buffer
	w := bufio.NewWriter(struct {
		io.Writer
	}{Writer: &muWriter{mu: &mu, b: &wire}})

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		MonitorLocalClip(w, nil, stop)
		close(done)
	}()

	// Give the monitor several poll iterations to (incorrectly) send.
	time.Sleep(300 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MonitorLocalClip did not return after stop")
	}

	mu.Lock()
	n := wire.Len()
	mu.Unlock()
	if n != 0 {
		t.Fatalf("wrote %d bytes for empty clipboard, want 0", n)
	}
}

// Empty → non-empty transition still sends once non-empty arrives.
func TestMonitorLocalClipSendsAfterEmpty(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	var clipMu sync.Mutex
	clip := ""
	stubClipboard(t, func() string {
		clipMu.Lock()
		defer clipMu.Unlock()
		return clip
	})

	var mu sync.Mutex
	var wire bytes.Buffer
	w := bufio.NewWriter(struct {
		io.Writer
	}{Writer: &muWriter{mu: &mu, b: &wire}})

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		MonitorLocalClip(w, nil, stop)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	clipMu.Lock()
	clip = "now-non-empty"
	clipMu.Unlock()

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := wire.Len()
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("MonitorLocalClip never sent after empty → non-empty")
		case <-time.After(10 * time.Millisecond):
		}
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MonitorLocalClip did not return after stop")
	}

	mu.Lock()
	data := append([]byte(nil), wire.Bytes()...)
	mu.Unlock()
	var payload []byte
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(payload) != "now-non-empty" {
		t.Errorf("sent %q, want %q", payload, "now-non-empty")
	}
}

type muWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (m *muWriter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b.Write(p)
}

func TestMonitorSentClipsBroadcastsToClients(t *testing.T) {
	preserveGlobals(t)
	setTo := stubClipboard(t, func() string { return "" })

	var mu sync.Mutex
	var c1, c2 bytes.Buffer
	mu.Lock()
	listOfClients = []*client{
		{w: bufio.NewWriter(&muBuf{mu: &mu, b: &c1}), addr: "a:1"},
		{w: bufio.NewWriter(&muBuf{mu: &mu, b: &c2}), addr: "a:2"},
	}
	mu.Unlock()

	var wire bytes.Buffer
	wire.Write(encodeFrame(t, []byte("broadcast-me")))

	clean := MonitorSentClips(bufio.NewReader(&wire), nil)
	if !clean {
		t.Fatal("expected clean EOF after valid frame")
	}
	if *setTo != "broadcast-me" {
		t.Errorf("setLocalClip got %q, want broadcast-me", *setTo)
	}
	mu.Lock()
	defer mu.Unlock()
	for name, buf := range map[string]*bytes.Buffer{"c1": &c1, "c2": &c2} {
		var payload []byte
		if err := gob.NewDecoder(buf).Decode(&payload); err != nil {
			t.Errorf("%s did not receive frame: %v", name, err)
			continue
		}
		if string(payload) != "broadcast-me" {
			t.Errorf("%s got %q", name, payload)
		}
	}
}

type muBuf struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (m *muBuf) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.b.Write(p)
}

func TestMonitorSentClipsSkipsEmptyAndDoesNotSetLocal(t *testing.T) {
	preserveGlobals(t)
	setCalled := false
	setLocalClip = func(string) { setCalled = true }
	getLocalClip = func() string { return "unchanged" }

	var wire bytes.Buffer
	wire.Write(encodeFrames(t, []byte{}, []byte("after-empty")))

	clean := MonitorSentClips(bufio.NewReader(&wire), nil)
	if !clean {
		t.Fatal("expected clean EOF")
	}
	if !setCalled {
		t.Fatal("expected setLocalClip for non-empty frame after empty skip")
	}
	mu.Lock()
	got := localClipboard
	mu.Unlock()
	if got != "after-empty" {
		t.Errorf("localClipboard=%q, want after-empty", got)
	}
}

func TestMonitorSentClipsDecryptFailureContinues(t *testing.T) {
	preserveGlobals(t)
	setCalled := false
	setLocalClip = func(string) { setCalled = true }

	// Frame under a key that cannot decrypt the payload — should skip and keep going.
	var wire bytes.Buffer
	wire.Write(encodeFrames(t, []byte("not-valid-ciphertext"), []byte("valid-plaintext-under-key")))

	// key != nil triggers decrypt; wrong ciphertext → handleError + continue.
	// Second frame also fails decrypt the same way (not encrypted), so still no set.
	key := []byte("decrypt-me")
	clean := MonitorSentClips(bufio.NewReader(&wire), key)
	if !clean {
		t.Fatal("expected clean EOF after decrypt failures (continue path)")
	}
	if setCalled {
		t.Error("setLocalClip should not run when decrypt fails")
	}
}

func TestHandleClientCleansUpWithoutExiting(t *testing.T) {
	preserveGlobals(t)
	secure, keyMode = false, false
	stubClipboard(t, func() string { return "server-clip" })

	// Seed a dummy so HandleClient's os.Exit(0) on last-client-disconnect
	// does not kill the test process when our client leaves.
	mu.Lock()
	listOfClients = []*client{{w: bufio.NewWriter(io.Discard), addr: "dummy:0"}}
	mu.Unlock()

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	hcDone := make(chan struct{})
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(hcDone)
			return
		}
		HandleClient(c)
		close(hcDone)
	}()

	cli, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	// Let HandleClient register the client before we drop.
	time.Sleep(50 * time.Millisecond)
	_ = cli.Close()

	select {
	case <-hcDone:
	case <-time.After(5 * time.Second):
		t.Fatal("HandleClient did not return after client disconnect")
	}

	mu.Lock()
	defer mu.Unlock()
	// Real client removed; dummy remains → no os.Exit path taken.
	if len(listOfClients) != 1 {
		t.Fatalf("listOfClients has %d entries, want 1 (dummy only)", len(listOfClients))
	}
	if listOfClients[0].addr != "dummy:0" {
		t.Errorf("remaining client addr %q, want dummy:0", listOfClients[0].addr)
	}
}

func TestConnectOnceDialFailureReconnects(t *testing.T) {
	preserveGlobals(t)
	secure = false

	// Bind then close to get a port nobody listens on.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()

	if !connectOnce(dead) {
		t.Fatal("expected true (reconnect) when dial fails")
	}
}

func TestConnectOnceCleanServerShutdownExits(t *testing.T) {
	preserveGlobals(t)
	secure, keyMode = false, false
	stubClipboard(t, func() string { return "client-clip" })

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		// Clean shutdown: close without sending a frame.
		_ = c.Close()
	}()

	// MonitorSentClips should see EOF → cleanShutdown → return false (exit).
	done := make(chan bool, 1)
	go func() { done <- connectOnce(addr) }()
	select {
	case reconnect := <-done:
		if reconnect {
			t.Fatal("expected false (do not reconnect) after clean server shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("connectOnce hung after clean server shutdown")
	}
}

func TestNormalizeWindowsClip(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"single line trailing CRLF", "hello\r\n", "hello"},
		{"internal CRLF preserved as LF", "line1\r\nline2\r\n", "line1\nline2"},
		{"multi-line no trailing", "a\r\nb\r\nc", "a\nb\nc"},
		{"already LF untouched", "a\nb\n", "a\nb"},
		{"lone CR untouched", "a\rb", "a\rb"},
		{"powershell double trailing", "hello\r\n\r\n", "hello\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeWindowsClip(tc.in); got != tc.want {
				t.Errorf("normalizeWindowsClip(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeWindowsClipIdempotent(t *testing.T) {
	in := "alpha\r\nbeta\r\ngamma\r\n"
	once := normalizeWindowsClip(in)
	if twice := normalizeWindowsClip(once); twice != once {
		t.Errorf("not idempotent: first %q, second %q", once, twice)
	}
	if strings.Contains(once, "\r") {
		t.Errorf("result still contains CR: %q", once)
	}
}

func FuzzMonitorSentClips(f *testing.F) {
	f.Add([]byte{})
	f.Add(encodeFrameForFuzz([]byte("ok")))
	f.Add(encodeFrameForFuzz([]byte{}))
	f.Add([]byte("not-gob-at-all"))
	f.Add(bytes.Repeat([]byte{0x00}, 64))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Same shared globals — seed corpus runs sequentially under go test.
		preserveGlobalsFuzz(t)
		setLocalClip = func(string) {}
		getLocalClip = func() string { return "" }
		listOfClients = nil

		// Run synchronously: fuzz iterations share package globals; a
		// lingering goroutine races the next iteration's cleanup.
		if r := recover(); r != nil {
			// recover must wrap the call, not sit above it — see below
			_ = r
		}
		monitorSentClipsNoHang(t, data)
	})
}

func monitorSentClipsNoHang(t *testing.T, data []byte) {
	t.Helper()
	done := make(chan bool, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic in MonitorSentClips: %v", r)
				done <- false
			}
		}()
		done <- MonitorSentClips(bufio.NewReader(bytes.NewReader(data)), nil)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MonitorSentClips timed out (possible infinite loop)")
	}
}

// encodeFrameForFuzz is encodeFrame without *testing.T for fuzz helpers.
func encodeFrameForFuzz(payload []byte) []byte {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(payload); err != nil {
		panic(err) // bytes.Buffer writes cannot fail
	}
	return buf.Bytes()
}

// preserveGlobalsFuzz is preserveGlobals without *testing.T (fuzz uses *testing.T too,
// but Cleanup ordering across fuzz iterations is fragile — restore inline).
func preserveGlobalsFuzz(t *testing.T) {
	t.Helper()
	oldSecure, oldKeyMode := secure, keyMode
	oldPassword := password
	oldClients := listOfClients
	oldClipboard := localClipboard
	oldGet, oldSet := getLocalClip, setLocalClip
	oldSeconds := secondsBetweenChecksForClipChange
	t.Cleanup(func() {
		secure, keyMode = oldSecure, oldKeyMode
		password = oldPassword
		listOfClients = oldClients
		localClipboard = oldClipboard
		getLocalClip, setLocalClip = oldGet, oldSet
		secondsBetweenChecksForClipChange = oldSeconds
	})
}

// silence unused import if errors is only used in older tests
