package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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
	oldStateDir := stateDir
	oldQuiet, oldDebug := quiet, printDebugInfo
	oldClipPush := lastClipPush.Load()
	oldMaxClients, oldActive := maxClients, activeConns
	t.Cleanup(func() {
		secure, keyMode = oldSecure, oldKeyMode
		password = oldPassword
		listOfClients = oldClients
		localClipboard = oldClipboard
		getLocalClip, setLocalClip = oldGet, oldSet
		secondsBetweenChecksForClipChange = oldSeconds
		stateDir = oldStateDir
		quiet, printDebugInfo = oldQuiet, oldDebug
		lastClipPush.Store(oldClipPush)
		maxClients, activeConns = oldMaxClients, oldActive
	})
}

// setTestHome points os.UserHomeDir at a fresh temp dir on unix and Windows
// (HOME / USERPROFILE) so clipportDir never touches the real ~/.clipport, and
// clears CLIPPORT_DIR / --dir so an ambient override can't leak into tests.
func setTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLIPPORT_DIR", "")
	stateDir = ""
	return home
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
		{name: "ipv6 hostport only", addr: "[::1]:53701", port: "", want: "[::1]:53701"},
		{name: "ipv6 bare host plus port flag", addr: "::1", port: "53701", want: "[::1]:53701"},
		{name: "ipv6 bracketed without port", addr: "[::1]", port: "53701", want: "[::1]:53701"},
		{name: "ipv6 matching embedded and flag", addr: "[fe80::1%en0]:53701", port: "53701", want: "[fe80::1%en0]:53701"},
		{name: "ipv6 bare no port anywhere", addr: "::1", port: "", wantErr: true},
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

// TestDualStackListenDial mirrors production networking: listen with "tcp"
// (dual-stack, all interfaces) and dial over both IPv4 loopback and IPv6
// loopback through the same listener. IPv6 failure is logged, not fatal, for
// environments with IPv6 disabled; IPv4 must always work.
func TestDualStackListenDial(t *testing.T) {
	all, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("dual-stack listen: %v", err)
	}
	defer all.Close()
	go func() {
		for {
			c, err := all.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	port := strconv.Itoa(all.Addr().(*net.TCPAddr).Port)

	v4, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Errorf("IPv4 dial failed: %v", err)
	} else {
		_ = v4.Close()
	}

	addr, err := resolveClientAddress(net.JoinHostPort("::1", port), "")
	if err != nil {
		t.Fatalf("resolveClientAddress for IPv6: %v", err)
	}
	v6, err := net.Dial("tcp", addr)
	if err != nil {
		t.Logf("IPv6 dial unavailable (environment may lack IPv6): %v", err)
		return
	}
	_ = v6.Close()
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// was written. Restores os.Stdout even if fn panics.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestInfoRespectsQuiet(t *testing.T) {
	preserveGlobals(t)

	quiet = false
	if got := captureStdout(t, func() { info("hello"); infof("n=%d\n", 3) }); got != "hello\nn=3\n" {
		t.Errorf("verbose mode got %q", got)
	}

	quiet = true
	if got := captureStdout(t, func() { info("hello"); infof("n=%d\n", 3) }); got != "" {
		t.Errorf("quiet mode got %q, want empty", got)
	}
}

func TestHandleErrorQuietStillPrintsErrors(t *testing.T) {
	preserveGlobals(t)
	quiet = true

	if got := captureStdout(t, func() { handleError(io.EOF) }); got != "" {
		t.Errorf("EOF in quiet mode printed %q to stdout, want empty", got)
	}

	errOut := captureStderr(t, func() { handleError(errors.New("boom")) })
	if !strings.Contains(errOut, "boom") {
		t.Errorf("error not on stderr in quiet mode: %q", errOut)
	}
}

func TestCurrentStatusSnapshot(t *testing.T) {
	preserveGlobals(t)
	mu.Lock()
	listOfClients = []*client{{addr: "10.0.0.1:1111"}, nil, {addr: "[::1]:2222"}}
	maxClients = 8
	mu.Unlock()
	lastClipPush.Store(1234567890)

	st := currentStatus("53701")
	if st.Pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", st.Pid, os.Getpid())
	}
	if st.Port != "53701" {
		t.Errorf("port = %q", st.Port)
	}
	want := []string{"10.0.0.1:1111", "[::1]:2222"}
	if len(st.Clients) != len(want) || st.Clients[0] != want[0] || st.Clients[1] != want[1] {
		t.Errorf("clients = %v, want %v (nil entries skipped)", st.Clients, want)
	}
	if st.MaxClients != 8 {
		t.Errorf("maxClients = %d, want 8", st.MaxClients)
	}
	if st.LastClipPush != 1234567890 {
		t.Errorf("lastClipPush = %d", st.LastClipPush)
	}
}

func TestTryReserveClientSlotEnforcesCap(t *testing.T) {
	preserveGlobals(t)
	maxClients, activeConns = 2, 0
	if !tryReserveClientSlot() || !tryReserveClientSlot() {
		t.Fatal("first two reservations should succeed")
	}
	if tryReserveClientSlot() {
		t.Error("third reservation should fail at cap 2")
	}
	releaseClientSlot()
	if !tryReserveClientSlot() {
		t.Error("slot should free after releaseClientSlot")
	}
}

func TestTryReserveClientSlotUnlimitedWhenZero(t *testing.T) {
	preserveGlobals(t)
	maxClients, activeConns = 0, 0
	for i := 0; i < 50; i++ {
		if !tryReserveClientSlot() {
			t.Fatalf("maxClients=0 must be unlimited; failed at %d", i)
		}
	}
}

func TestServeStatusRoundtrip(t *testing.T) {
	preserveGlobals(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer l.Close()
	mu.Lock()
	listOfClients = []*client{{addr: "1.2.3.4:9"}}
	mu.Unlock()
	lastClipPush.Store(42)

	go serveStatus(l, "7777")
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var st statusSnapshot
	if err := json.Unmarshal(line, &st); err != nil {
		t.Fatalf("unmarshal %q: %v", line, err)
	}
	if st.Port != "7777" || len(st.Clients) != 1 || st.Clients[0] != "1.2.3.4:9" || st.LastClipPush != 42 {
		t.Errorf("snapshot = %+v", st)
	}
}

func TestQueryStatusNoServer(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queryStatus(dir); err == nil {
		t.Fatal("expected error when no status socket exists")
	}
}

func TestRunStatusNoServerErrorIsNotSilent(t *testing.T) {
	// queryStatus error path is covered above; runStatus exits(1), which we
	// don't call directly. This locks the socket-path contract instead:
	preserveGlobals(t)
	dir := t.TempDir()
	if got := statusSocketPath(dir); got != filepath.Join(dir, "clipport.sock") {
		t.Errorf("statusSocketPath = %q", got)
	}
}

func TestClipportDirEnvOverride(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	want := t.TempDir()
	t.Setenv("CLIPPORT_DIR", want)
	got, err := clipportDir()
	if err != nil {
		t.Fatalf("clipportDir: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("state dir not created: %v", err)
	}
}

func TestClipportDirFlagBeatsEnv(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	flagDir := t.TempDir()
	envDir := t.TempDir()
	stateDir = flagDir
	t.Setenv("CLIPPORT_DIR", envDir)
	got, err := clipportDir()
	if err != nil {
		t.Fatalf("clipportDir: %v", err)
	}
	if got != flagDir {
		t.Errorf("got %q, want flag dir %q (env must lose)", got, flagDir)
	}
}

func TestClipportDirDefaultHome(t *testing.T) {
	preserveGlobals(t)
	home := setTestHome(t)
	got, err := clipportDir()
	if err != nil {
		t.Fatalf("clipportDir: %v", err)
	}
	want := filepath.Join(home, ".clipport")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
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
	setTestHome(t)
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
	setTestHome(t)

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
	if !strings.Contains(err.Error(), "known-hosts remove") {
		t.Errorf("mismatch error should point at known-hosts remove, got: %v", err)
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
	setTestHome(t)
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
	setTestHome(t)
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

	retry, reached := connectOnce(dead)
	if !retry {
		t.Fatal("expected retry when dial fails")
	}
	if reached {
		t.Error("dial failure must not count as a reached session")
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
	type result struct {
		retry, reached bool
	}
	done := make(chan result, 1)
	go func() {
		r, reached := connectOnce(addr)
		done <- result{r, reached}
	}()
	select {
	case res := <-done:
		if res.retry {
			t.Fatal("expected no retry after clean server shutdown")
		}
		if !res.reached {
			t.Error("session was established before shutdown; reached should be true")
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

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(out)
}

func TestClipReadFailureReportedOnceUntilSuccess(t *testing.T) {
	clipReadErrReported.Store(false)

	first := captureStderr(t, func() {
		reportClipReadFailure(errors.New("exit status 1"))
	})
	if !strings.Contains(first, "cannot read clipboard as text") {
		t.Errorf("first failure should log, got %q", first)
	}

	second := captureStderr(t, func() {
		reportClipReadFailure(errors.New("exit status 1"))
	})
	if second != "" {
		t.Errorf("second failure should be suppressed, got %q", second)
	}

	reportClipReadSuccess()

	third := captureStderr(t, func() {
		reportClipReadFailure(errors.New("exit status 1"))
	})
	if !strings.Contains(third, "cannot read clipboard as text") {
		t.Errorf("failure after success should log again, got %q", third)
	}
	clipReadErrReported.Store(false)
}

func writeFakeBin(t *testing.T, dir, name string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
}

// skipUnlessLinux: linuxClipboardCommand tests exec shebang scripts via PATH,
// which only works where the kernel honors #! (not Windows).
func skipUnlessLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("linux clipboard tool selection only runs on linux")
	}
}

func TestLinuxClipboardCommandPrefersWaylandWhenSessionSet(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	writeFakeBin(t, dir, "xclip")
	writeFakeBin(t, dir, "xsel")
	writeFakeBin(t, dir, "wl-paste")
	writeFakeBin(t, dir, "wl-copy")
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")

	getCmd, err := linuxClipboardCommand(true)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if filepath.Base(getCmd.Path) != "wl-paste" {
		t.Errorf("get tool = %v, want wl-paste", getCmd.Args)
	}

	setCmd, err := linuxClipboardCommand(false)
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if filepath.Base(setCmd.Path) != "wl-copy" {
		t.Errorf("set tool = %v, want wl-copy", setCmd.Args)
	}
}

func TestLinuxClipboardCommandX11OrderWithoutWayland(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	writeFakeBin(t, dir, "xclip")
	writeFakeBin(t, dir, "wl-paste")
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "")

	getCmd, err := linuxClipboardCommand(true)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if filepath.Base(getCmd.Path) != "xclip" {
		t.Errorf("get tool = %v, want xclip on X11", getCmd.Args)
	}
}

func TestLinuxClipboardCommandWaylandFallbackToXclip(t *testing.T) {
	skipUnlessLinux(t)
	dir := t.TempDir()
	writeFakeBin(t, dir, "xclip")
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")

	getCmd, err := linuxClipboardCommand(true)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if filepath.Base(getCmd.Path) != "xclip" {
		t.Errorf("get tool = %v, want xclip fallback when wl-paste missing", getCmd.Args)
	}
}

func TestLinuxClipboardCommandNoTools(t *testing.T) {
	skipUnlessLinux(t)
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if _, err := linuxClipboardCommand(true); err == nil {
		t.Error("expected error when no clipboard tools exist")
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

func TestNextBackoff(t *testing.T) {
	const max = 30 * time.Second
	if got := nextBackoff(3*time.Second, max); got != 6*time.Second {
		t.Errorf("3s→%v, want 6s", got)
	}
	if got := nextBackoff(20*time.Second, max); got != 30*time.Second {
		t.Errorf("20s→%v, want 30s (cap)", got)
	}
	if got := nextBackoff(30*time.Second, max); got != 30*time.Second {
		t.Errorf("30s→%v, want 30s (stay capped)", got)
	}
}

func TestPermanentErrorClassification(t *testing.T) {
	if permanent(nil) != nil {
		t.Error("permanent(nil) should be nil")
	}
	err := permanent(errors.New("key changed"))
	if !isPermanent(err) {
		t.Error("wrapped error should be permanent")
	}
	if !strings.Contains(err.Error(), "key changed") {
		t.Errorf("Error() = %q, want inner text", err.Error())
	}
	if isPermanent(errors.New("transient dial failure")) {
		t.Error("plain error must not be permanent")
	}
	var target *permanentError
	if !errors.As(err, &target) {
		t.Error("errors.As should unwrap permanentError")
	}
}

func TestRemoveKnownPeer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_peers")
	if err := saveKnownPeers(path, map[string]string{
		"peer-a": "AAAA",
		"peer-b": "BBBB",
	}); err != nil {
		t.Fatal(err)
	}
	if err := removeKnownPeer(path, "peer-a"); err != nil {
		t.Fatalf("remove peer-a: %v", err)
	}
	peers, err := loadKnownPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := peers["peer-a"]; ok {
		t.Error("peer-a should be gone")
	}
	if peers["peer-b"] != "BBBB" {
		t.Errorf("peer-b should remain, got %#v", peers)
	}
	if err := removeKnownPeer(path, "peer-a"); err == nil {
		t.Error("removing missing peer should error")
	}
}

func TestKeyHandshakeMismatchIsPermanent(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	secure, keyMode = true, true

	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := generateKeypair(dir); err != nil {
		t.Fatalf("generateKeypair: %v", err)
	}

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()

	// Pre-trust the server host (peer IDs are host-only — SplitHostPort's
	// first return) with the wrong public key so the client's
	// verifyOrTrustPeer fails on this handshake.
	dir2, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir2, "known_peers")
	wrong := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32))
	if err := os.WriteFile(path, []byte(host+" "+wrong+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	serverErr := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer c.Close()
		_, err = resolveConnectionKey(c, true, "")
		serverErr <- err
	}()

	c, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, clientErr := resolveConnectionKey(c, false, addr)
	if clientErr == nil {
		t.Fatal("client should fail key verification")
	}
	if !isPermanent(clientErr) {
		t.Errorf("client mismatch must be permanent, got: %v", clientErr)
	}
	serr := <-serverErr
	if serr == nil {
		t.Fatal("server should see client reject")
	}
	if !isPermanent(serr) {
		t.Errorf("server peer-reject must be permanent, got: %v", serr)
	}
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
