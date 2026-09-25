package main

import (
	"bufio"
	"bytes"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	oldClipKind := lastClipKindString()
	oldClipBytes := lastClipBytes.Load()
	oldMaxClients, oldActive := maxClients, activeConns
	oldDebounce := clipboardDebounce
	oldWakeGap := wakeGapThreshold
	oldWoke := systemWoke.Load()
	oldClock := clockNow
	oldGrace := emptyDisconnectGrace
	oldExit := exitProcess
	oldWriteImage := writeImageClip
	oldProbeTimeout := staleProbeTimeout
	oldWakePoll, oldWakeSettle := serverWakePoll, serverWakeSettle
	oldPruneStale := pruneStale
	oldSlotTries := slotReleaseTries
	oldPruneCooldown := prunePassCooldown
	oldLastPrune := lastPrunePass.Load()
	lastPrunePass.Store(0)
	oldPrunedCount := prunedClients.Load()
	prunedClients.Store(0)
	oldClientProc := isClientProcess.Load()
	oversizeFrameReported.Store(false)
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
		lastClipKind.Store(oldClipKind)
		lastClipBytes.Store(oldClipBytes)
		maxClients, activeConns = oldMaxClients, oldActive
		clipboardDebounce = oldDebounce
		wakeGapThreshold = oldWakeGap
		systemWoke.Store(oldWoke)
		clockNow = oldClock
		emptyDisconnectGrace = oldGrace
		exitProcess = oldExit
		writeImageClip = oldWriteImage
		staleProbeTimeout = oldProbeTimeout
		serverWakePoll, serverWakeSettle = oldWakePoll, oldWakeSettle
		pruneStale = oldPruneStale
		slotReleaseTries = oldSlotTries
		prunePassCooldown = oldPruneCooldown
		lastPrunePass.Store(oldLastPrune)
		prunedClients.Store(oldPrunedCount)
		isClientProcess.Store(oldClientProc)
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
	lastClipKind.Store(clipKindImage)
	lastClipBytes.Store(4321)
	prunedClients.Store(5)

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
	if st.LastClipKind != clipKindImage || st.LastClipBytes != 4321 {
		t.Errorf("payload = %q/%d, want image/4321", st.LastClipKind, st.LastClipBytes)
	}
	if st.Pruned != 5 {
		t.Errorf("pruned = %d, want 5", st.Pruned)
	}
}

func TestTryReserveClientSlotEnforcesCap(t *testing.T) {
	preserveGlobals(t)
	maxClients, activeConns = 2, 0
	first, second := tryReserveClientSlot(), tryReserveClientSlot()
	if !first || !second {
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

func TestReserveClientSlotSkipsPruneWhenNotFull(t *testing.T) {
	preserveGlobals(t)
	maxClients, activeConns = 2, 0
	pruned := 0
	pruneStale = func() { pruned++ }

	if !reserveClientSlotWithPrune() {
		t.Fatal("reservation should succeed without pruning when not full")
	}
	if pruned != 0 {
		t.Errorf("prune ran %d times, want 0", pruned)
	}
}

// releasingProbeConn stands in for HandleClient's cleanup: closing the dead
// conn releases the slot that connection still holds, the same way the real
// HandleClient does a moment after its monitor unblocks.
type releasingProbeConn struct {
	fakeProbeConn
}

func (r *releasingProbeConn) Close() error {
	if err := r.fakeProbeConn.Close(); err != nil {
		return err
	}
	releaseClientSlot()
	return nil
}

// A full server runs one stale-probe pass and reserves the freed slot instead
// of rejecting the joiner.
func TestReserveClientSlotPrunesWhenFull(t *testing.T) {
	preserveGlobals(t)
	dead := &releasingProbeConn{fakeProbeConn: fakeProbeConn{writeErr: errors.New("broken pipe")}}
	mu.Lock()
	maxClients, activeConns = 1, 1
	listOfClients = []*client{{w: bufio.NewWriter(dead), addr: "dead:1", conn: dead}}
	mu.Unlock()

	if !reserveClientSlotWithPrune() {
		t.Fatal("full server should reserve a slot after pruning the write-dead peer")
	}
	if !dead.closed.Load() {
		t.Error("write-dead peer was not closed by the prune pass")
	}
	if got := prunedClients.Load(); got != 1 {
		t.Errorf("prunedClients = %d, want 1 (slot reclaimed by prune-on-full)", got)
	}
	mu.Lock()
	got := activeConns
	mu.Unlock()
	if got != 1 {
		t.Errorf("activeConns = %d, want 1 (dead slot released, then re-reserved)", got)
	}
}

// When pruning frees nothing, the joiner is still rejected — and prune runs
// exactly once, so a flood of joiners cannot turn the accept loop into
// continuous probing.
func TestReserveClientSlotStillFullAfterPrune(t *testing.T) {
	preserveGlobals(t)
	slotReleaseTries = 2
	pruned := 0
	pruneStale = func() { pruned++ }
	mu.Lock()
	maxClients, activeConns = 1, 1
	listOfClients = nil
	mu.Unlock()

	if reserveClientSlotWithPrune() {
		t.Error("reserve should fail when no stale peer frees a slot")
	}
	if pruned != 1 {
		t.Errorf("prune ran %d times, want exactly 1", pruned)
	}
}

// A flood of rejected joiners triggers one probe pass per cooldown window,
// not one probe pass per joiner; once the window elapses the next joiner
// probes again.
func TestPrunePassCooldownOnAcceptLoop(t *testing.T) {
	preserveGlobals(t)
	slotReleaseTries = 1
	pruned := 0
	pruneStale = func() { pruned++ }
	mu.Lock()
	maxClients, activeConns = 1, 1
	listOfClients = nil
	mu.Unlock()
	base := time.Now()
	fake := base
	clockNow = func() time.Time { return fake }

	if reserveClientSlotWithPrune() {
		t.Fatal("full server should reject the joiner")
	}
	if pruned != 1 {
		t.Fatalf("first rejection: prune ran %d times, want 1", pruned)
	}
	if reserveClientSlotWithPrune() {
		t.Fatal("full server should reject the second joiner too")
	}
	if pruned != 1 {
		t.Errorf("joiner inside cooldown: prune ran %d times, want 1 (probe pass skipped)", pruned)
	}
	fake = base.Add(prunePassCooldown + time.Second)
	if reserveClientSlotWithPrune() {
		t.Fatal("full server should reject the third joiner too")
	}
	if pruned != 2 {
		t.Errorf("joiner after cooldown: prune ran %d times, want 2", pruned)
	}
}

// A wake-triggered probe pass refreshes the same cooldown window, so a
// joiner arriving right after a wake is not probed a second time.
func TestPrunePassCooldownSharedWithWake(t *testing.T) {
	preserveGlobals(t)
	slotReleaseTries = 1
	pruned := 0
	pruneStale = func() { pruned++ }
	mu.Lock()
	maxClients, activeConns = 1, 1
	listOfClients = nil
	mu.Unlock()

	runPrunePass() // what the wake watcher runs after resume
	if pruned != 1 {
		t.Fatalf("wake pass: prune ran %d times, want 1", pruned)
	}
	if reserveClientSlotWithPrune() {
		t.Fatal("full server should reject the joiner")
	}
	if pruned != 1 {
		t.Errorf("joiner right after wake pass: prune ran %d times, want 1 (shared window)", pruned)
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
	lastClipKind.Store(clipKindText)
	lastClipBytes.Store(17)
	prunedClients.Store(9)

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
	if st.Port != "7777" || len(st.Clients) != 1 || st.Clients[0] != "1.2.3.4:9" || st.LastClipPush != 42 || st.LastClipKind != clipKindText || st.LastClipBytes != 17 || st.Pruned != 9 {
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

// `clipport status` prints the prune counter only when slots were actually
// reclaimed — visible after a wake/joiner prune pass, quiet otherwise.
func TestRunStatusPruneLine(t *testing.T) {
	preserveGlobals(t)
	dir := t.TempDir()
	stateDir = dir
	l, err := net.Listen("unix", statusSocketPath(dir))
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer l.Close()
	go serveStatus(l, "7777")

	prunedClients.Store(7)
	out := captureStdout(t, runStatus)
	if !strings.Contains(out, "Stale clients pruned: 7") {
		t.Errorf("output missing prune line: %q", out)
	}

	prunedClients.Store(0)
	out = captureStdout(t, runStatus)
	if strings.Contains(out, "pruned") {
		t.Errorf("prune line should be hidden at zero: %q", out)
	}
}

// The last-pushed line carries payload kind + human size; a status server
// that predates payload-kind reporting (empty kind) keeps the plain line.
func TestRunStatusPayloadLine(t *testing.T) {
	preserveGlobals(t)
	dir := t.TempDir()
	stateDir = dir
	l, err := net.Listen("unix", statusSocketPath(dir))
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer l.Close()
	go serveStatus(l, "7777")

	lastClipPush.Store(time.Now().UnixNano())
	lastClipKind.Store(clipKindImage)
	lastClipBytes.Store(2048)
	out := captureStdout(t, runStatus)
	if !strings.Contains(out, "Clipboard last pushed:") || !strings.Contains(out, "(image, 2.0 KiB)") {
		t.Errorf("payload line missing kind/size: %q", out)
	}

	lastClipKind.Store("") // older server: kind field absent
	out = captureStdout(t, runStatus)
	if !strings.Contains(out, "Clipboard last pushed:") || strings.Contains(out, "(image") {
		t.Errorf("plain pushed line expected for empty kind: %q", out)
	}
}

func TestFormatByteSize(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{3 << 30, "3.0 GiB"},
	}
	for _, c := range cases {
		if got := formatByteSize(c.n); got != c.want {
			t.Errorf("formatByteSize(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// fakeClipboardPATH points PATH at stub tools for the current platform so
// clipboardBackendDetail's LookPath probes succeed (stubs are never executed).
func fakeClipboardPATH(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	switch runtime.GOOS {
	case "darwin":
		writeFakeBin(t, dir, "pbpaste")
		writeFakeBin(t, dir, "pbcopy")
	case "windows":
		// LookPath on windows only checks presence, not content; "clip"
		// resolves through PATHEXT, so the stub must be clip.exe
		for _, name := range []string{"powershell.exe", "clip.exe"} {
			if err := os.WriteFile(filepath.Join(dir, name), nil, 0o755); err != nil {
				t.Fatalf("write fake %s: %v", name, err)
			}
		}
	default:
		// xclip is first in the get and set order, one stub covers both
		writeFakeBin(t, dir, "xclip")
	}
	t.Setenv("PATH", dir)
	t.Setenv("WAYLAND_DISPLAY", "")
}

func TestClipboardBackendDetailMissingTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("WAYLAND_DISPLAY", "")
	if _, err := clipboardBackendDetail(); err == nil {
		t.Fatal("expected error when no clipboard tool is on PATH")
	}
}

func TestClipboardBackendDetailReportsTool(t *testing.T) {
	fakeClipboardPATH(t)
	detail, err := clipboardBackendDetail()
	if err != nil {
		t.Fatalf("clipboardBackendDetail: %v", err)
	}
	if detail == "" {
		t.Fatal("empty backend detail")
	}
}

func TestCollectDoctorChecksNoServerAllOK(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	fakeClipboardPATH(t)
	checks := collectDoctorChecks("")
	wantNames := []string{"clipboard backend", "state dir", "keypair", "known-hosts", "server", "listener"}
	if len(checks) != len(wantNames) {
		t.Fatalf("got %d checks, want %d: %+v", len(checks), len(wantNames), checks)
	}
	for i, want := range wantNames {
		if checks[i].name != want {
			t.Errorf("check %d = %q, want %q", i, checks[i].name, want)
		}
		if checks[i].status == "fail" {
			t.Errorf("check %q failed: %s", checks[i].name, checks[i].detail)
		}
	}
	if !strings.Contains(checks[2].detail, "absent") {
		t.Errorf("keypair detail = %q, want absent", checks[2].detail)
	}
	if !strings.Contains(checks[4].detail, "not running") {
		t.Errorf("server detail = %q, want not running", checks[4].detail)
	}
}

func TestCollectDoctorChecksKeyPairPresent(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	fakeClipboardPATH(t)
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := generateKeypair(dir); err != nil {
		t.Fatalf("generateKeypair: %v", err)
	}
	for _, c := range collectDoctorChecks("") {
		if c.name == "keypair" {
			if c.status != "ok" || !strings.Contains(c.detail, "present (fingerprint") {
				t.Errorf("keypair check = %+v, want ok present with fingerprint", c)
			}
			return
		}
	}
	t.Fatal("keypair check not found")
}

func TestCollectDoctorChecksListenerBusy(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	fakeClipboardPATH(t)
	held, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("hold port: %v", err)
	}
	defer held.Close()
	port := strconv.Itoa(held.Addr().(*net.TCPAddr).Port)
	for _, c := range collectDoctorChecks(port) {
		if c.name == "listener" {
			if c.status != "fail" || !strings.Contains(c.detail, "cannot bind") {
				t.Errorf("listener check = %+v, want fail cannot bind", c)
			}
			return
		}
	}
	t.Fatal("listener check not found")
}

func TestCollectDoctorChecksRunningServer(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	fakeClipboardPATH(t)
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listener: %v", err)
	}
	defer tcp.Close()
	port := strconv.Itoa(tcp.Addr().(*net.TCPAddr).Port)
	statusL, err := net.Listen("unix", statusSocketPath(dir))
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer statusL.Close()
	go serveStatus(statusL, port)
	checks := collectDoctorChecks("")
	var server, listener *doctorCheck
	for i := range checks {
		switch checks[i].name {
		case "server":
			server = &checks[i]
		case "listener":
			listener = &checks[i]
		}
	}
	if server == nil || listener == nil {
		t.Fatalf("server/listener checks missing: %+v", checks)
	}
	if server.status != "ok" || !strings.Contains(server.detail, "running (pid") {
		t.Errorf("server check = %+v, want running", *server)
	}
	if listener.status != "ok" || !strings.Contains(listener.detail, "reachable on loopback") {
		t.Errorf("listener check = %+v, want reachable", *listener)
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

func TestPlaintextOptIn(t *testing.T) {
	if _, set := os.LookupEnv("CLIPPORT_ALLOW_PLAINTEXT"); set {
		old := os.Getenv("CLIPPORT_ALLOW_PLAINTEXT")
		t.Cleanup(func() { os.Setenv("CLIPPORT_ALLOW_PLAINTEXT", old) })
		os.Unsetenv("CLIPPORT_ALLOW_PLAINTEXT")
	} else {
		t.Cleanup(func() { os.Unsetenv("CLIPPORT_ALLOW_PLAINTEXT") })
	}
	if plaintextOptIn() {
		t.Error("unset CLIPPORT_ALLOW_PLAINTEXT must keep the prompt gate")
	}
	for _, val := range []string{"", "0", "true", "yes", "1\n"} {
		t.Setenv("CLIPPORT_ALLOW_PLAINTEXT", val)
		if plaintextOptIn() {
			t.Errorf("value %q must not bypass the prompt", val)
		}
	}
	t.Setenv("CLIPPORT_ALLOW_PLAINTEXT", "1")
	if !plaintextOptIn() {
		t.Error("CLIPPORT_ALLOW_PLAINTEXT=1 must skip the prompt")
	}
}

func TestPasswordFromFile(t *testing.T) {
	t.Setenv("CLIPPORT_SECRET", "")
	t.Setenv("CLIPPORT_PASSWORD", "")
	dir := t.TempDir()

	write := func(name, content string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	for name, want := range map[string]string{
		"plain":     "hunter2",
		"newline":   "hunter2",
		"crlf":      "hunter2",
		"multi-nl":  "hunter2",
		"space pad": " hunter2 ",
	} {
		content := want
		switch name {
		case "newline":
			content = "hunter2\n"
		case "crlf":
			content = "hunter2\r\n"
		case "multi-nl":
			content = "hunter2\n\n"
		}
		path := write(name, content)
		pw, found, err := passwordFromSources(path)
		if err != nil || !found || string(pw) != want {
			t.Errorf("passwordFromSources(%s): got (%q, %v, %v), want (%q, true, nil)", name, pw, found, err, want)
		}
	}

	for name, content := range map[string]string{"empty": "", "only-newlines": "\n\r\n"} {
		path := write(name, content)
		if _, _, err := passwordFromSources(path); err == nil {
			t.Errorf("password file %s must be rejected as empty", name)
		}
	}
	if _, _, err := passwordFromSources(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing password file must error")
	}
}

func TestPasswordEnvSources(t *testing.T) {
	t.Setenv("CLIPPORT_SECRET", "")
	t.Setenv("CLIPPORT_PASSWORD", "")

	if _, found, err := passwordFromSources(""); err != nil || found {
		t.Errorf("no sources: got found=%v err=%v, want found=false, nil", found, err)
	}

	t.Setenv("CLIPPORT_SECRET", "from-secret")
	pw, found, err := passwordFromSources("")
	if err != nil || !found || string(pw) != "from-secret" {
		t.Errorf("CLIPPORT_SECRET: got (%q, %v, %v)", pw, found, err)
	}
	t.Setenv("CLIPPORT_SECRET", "")
	t.Setenv("CLIPPORT_PASSWORD", "from-alias")
	pw, found, err = passwordFromSources("")
	if err != nil || !found || string(pw) != "from-alias" {
		t.Errorf("CLIPPORT_PASSWORD: got (%q, %v, %v)", pw, found, err)
	}

	t.Setenv("CLIPPORT_SECRET", "a")
	t.Setenv("CLIPPORT_PASSWORD", "b")
	if _, _, err := passwordFromSources(""); err == nil {
		t.Error("both env names set must error, not pick one silently")
	}

	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("from-file"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := passwordFromSources(path); err == nil {
		t.Error("file and environment together must error, not silently prefer one")
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
	entry, ok := peers["10.0.0.9:1234"]
	if !ok || entry.Key == "" {
		t.Fatal("known_peers missing trusted peer")
	}
	if entry.LastSeen == 0 {
		t.Error("trusted peer should have a last-seen stamp after handshake")
	}
}

func TestLoadKnownPeersSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_peers")
	content := "peer-a AAAA\n\nnot-a-peer-line\npeer-b BBBB\npeer-c CCCC 1758000000123456789\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	peers, err := loadKnownPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 3 {
		t.Fatalf("got %d peers, want 3 (malformed line skipped): %#v", len(peers), peers)
	}
	if peers["peer-a"].Key != "AAAA" || peers["peer-b"].Key != "BBBB" {
		t.Errorf("unexpected peers: %#v", peers)
	}
	// Legacy two-column lines parse with last-seen zero; the third column
	// is the optional stamp.
	if peers["peer-a"].LastSeen != 0 {
		t.Errorf("peer-a last-seen = %d, want 0 for legacy line", peers["peer-a"].LastSeen)
	}
	if peers["peer-c"].LastSeen != 1758000000123456789 {
		t.Errorf("peer-c last-seen = %d, want parsed stamp", peers["peer-c"].LastSeen)
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
	in := map[string]knownPeer{
		"p1": {Key: "K1", LastSeen: 1758000000000000000},
		"p2": {Key: "K2"}, // legacy-style: no stamp yet
	}
	if err := saveKnownPeers(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := loadKnownPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out["p1"].Key != "K1" || out["p2"].Key != "K2" {
		t.Fatalf("roundtrip mismatch: %#v", out)
	}
	if out["p1"].LastSeen != 1758000000000000000 || out["p2"].LastSeen != 0 {
		t.Errorf("last-seen roundtrip mismatch: %#v", out)
	}
}

// A reconnect to an already-trusted peer moves its last-seen stamp forward;
// a key mismatch must NOT (the handshake aborts before stamping).
func TestVerifyOrTrustPeerRestampsLastSeen(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "known_peers")
	pub := bytes.Repeat([]byte{0x42}, 32)
	encoded := base64.StdEncoding.EncodeToString(pub)
	if err := os.WriteFile(path, []byte("10.0.0.9:1234 "+encoded+" 42\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyOrTrustPeer("10.0.0.9:1234", pub); err != nil {
		t.Fatalf("same-key reconnect: %v", err)
	}
	peers, err := loadKnownPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	if peers["10.0.0.9:1234"].LastSeen <= 42 {
		t.Errorf("last-seen = %d, want restamped above the old 42", peers["10.0.0.9:1234"].LastSeen)
	}

	// Mismatch: rejected, so the stamp must stay as it was.
	afterStamp := peers["10.0.0.9:1234"].LastSeen
	changed := bytes.Repeat([]byte{0x99}, 32)
	if err := verifyOrTrustPeer("10.0.0.9:1234", changed); err == nil {
		t.Fatal("changed key should be rejected")
	}
	peers, err = loadKnownPeers(path)
	if err != nil {
		t.Fatal(err)
	}
	if peers["10.0.0.9:1234"].LastSeen != afterStamp {
		t.Errorf("last-seen = %d after rejected handshake, want unchanged %d", peers["10.0.0.9:1234"].LastSeen, afterStamp)
	}
}

func TestLastSeenLabel(t *testing.T) {
	if got := lastSeenLabel(0); got != labelNever {
		t.Errorf("lastSeenLabel(0) = %q, want never", got)
	}
	if got := lastSeenLabel(-1); got != labelNever {
		t.Errorf("lastSeenLabel(-1) = %q, want never", got)
	}
	got := lastSeenLabel(time.Now().Add(-90 * time.Second).UnixNano())
	if !strings.HasSuffix(got, " ago") || strings.Contains(got, "never") {
		t.Errorf("lastSeenLabel(recent) = %q, want a relative ago label", got)
	}
}

// `clipport known-hosts list` shows a per-peer last-seen label: stamped
// entries say how long ago, legacy entries say never.
func TestListKnownPeersShowsLastSeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_peers")
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
	content := "peer-stamped " + key + " " + strconv.FormatInt(time.Now().Add(-2*time.Minute).UnixNano(), 10) + "\n" +
		"peer-legacy " + key + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if err := listKnownPeers(path); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "peer-stamped") || !strings.Contains(out, "last seen") || !strings.Contains(out, " ago") {
		t.Errorf("stamped peer missing last-seen label: %q", out)
	}
	if !strings.Contains(out, "peer-legacy") || !strings.Contains(out, "never") {
		t.Errorf("legacy peer should show last seen never: %q", out)
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

func TestOwnFingerprintMatchesKeygen(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	_, pub, err := generateKeypair(dir)
	if err != nil {
		t.Fatalf("generateKeypair: %v", err)
	}

	fp, err := ownFingerprint()
	if err != nil {
		t.Fatalf("ownFingerprint: %v", err)
	}
	if fp != fingerprint(pub) {
		t.Errorf("ownFingerprint() = %q, want keygen's %q", fp, fingerprint(pub))
	}
}

func TestOwnFingerprintMissingKey(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	if _, err := ownFingerprint(); err == nil {
		t.Fatal("expected error when no key exists")
	} else if !strings.Contains(err.Error(), "keygen") {
		t.Errorf("error should point at keygen, got: %v", err)
	}
}

func TestRunKeyCommandUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}, {"fingerprint", "extra"}, {"rotate", "extra"}} {
		if err := runKeyCommand(args); err == nil || !strings.Contains(err.Error(), "usage: clipport key fingerprint") {
			t.Errorf("runKeyCommand(%v) = %v, want usage error", args, err)
		}
	}
}

func TestKeyRotate(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := generateKeypair(dir); err != nil {
		t.Fatalf("generateKeypair: %v", err)
	}
	oldRaw, err := os.ReadFile(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatalf("read old key: %v", err)
	}
	oldPriv, err := ecdh.X25519().NewPrivateKey(oldRaw)
	if err != nil {
		t.Fatalf("parse old key: %v", err)
	}

	if err := runKeyCommand([]string{"rotate"}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	fp, err := ownFingerprint()
	if err != nil {
		t.Fatalf("ownFingerprint after rotate: %v", err)
	}
	if fp == fingerprint(oldPriv.PublicKey().Bytes()) {
		t.Error("rotate must change the fingerprint")
	}
	if _, _, err := generateKeypair(dir); err == nil {
		t.Error("keygen must still refuse to overwrite the rotated key")
	}
}

func TestKeyRotateBackups(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	dir, err := clipportDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := generateKeypair(dir); err != nil {
		t.Fatalf("generateKeypair: %v", err)
	}
	oldRaw, err := os.ReadFile(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatalf("read old key: %v", err)
	}

	if err := runKeyCommand([]string{"rotate"}); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	allBackups, err := filepath.Glob(filepath.Join(dir, "key.*.bak"))
	if err != nil {
		t.Fatalf("glob key backups: %v", err)
	}
	var backups []string
	for _, m := range allBackups {
		if !strings.HasPrefix(filepath.Base(m), "key.pub.") {
			backups = append(backups, m)
		}
	}
	if len(backups) != 1 {
		t.Fatalf("want exactly one key backup, got %v", backups)
	}
	backupRaw, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if !bytes.Equal(backupRaw, oldRaw) {
		t.Error("backup must contain the previous private key verbatim")
	}
	pubBackups, err := filepath.Glob(filepath.Join(dir, "key.pub.*.bak"))
	if err != nil || len(pubBackups) != 1 {
		t.Errorf("want exactly one key.pub backup, got %v (err %v)", pubBackups, err)
	}
}

func TestKeyRotateMissingKey(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	if err := runKeyCommand([]string{"rotate"}); err == nil {
		t.Fatal("rotate with no key must error")
	} else if !strings.Contains(err.Error(), "keygen") {
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
	if got := lastClipKindString(); got != clipKindText {
		t.Errorf("lastClipKind = %q, want %q", got, clipKindText)
	}
	if got := lastClipBytes.Load(); got != int64(len("hello-from-local")) {
		t.Errorf("lastClipBytes = %d, want %d", got, len("hello-from-local"))
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

// A burst of local edits must coalesce into one frame carrying the final
// value — intermediate states never reach the wire.
func TestMonitorLocalClipDebouncesRapidChanges(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	clipboardDebounce = 100 * time.Millisecond

	var clipMu sync.Mutex
	clip := "one"
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

	decodeAll := func() []string {
		mu.Lock()
		data := append([]byte(nil), wire.Bytes()...)
		mu.Unlock()
		var out []string
		dec := gob.NewDecoder(bytes.NewReader(data))
		for {
			var p []byte
			if err := dec.Decode(&p); err != nil {
				break
			}
			out = append(out, string(p))
		}
		return out
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		MonitorLocalClip(w, nil, stop)
		close(done)
	}()

	// Wait for the initial snapshot frame before mutating the stub.
	deadline := time.After(3 * time.Second)
	for len(decodeAll()) < 1 {
		select {
		case <-deadline:
			t.Fatal("initial frame never sent")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Burst completes well before the 1s change-poll observes it.
	for _, v := range []string{"two", "three", "four"} {
		clipMu.Lock()
		clip = v
		clipMu.Unlock()
		time.Sleep(30 * time.Millisecond)
	}

	deadline = time.After(3 * time.Second)
	for len(decodeAll()) < 2 {
		select {
		case <-deadline:
			t.Fatalf("debounced frame never sent; frames = %v", decodeAll())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Give any (incorrect) extra sends time to land before the final count.
	time.Sleep(300 * time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MonitorLocalClip did not return after stop")
	}

	frames := decodeAll()
	if len(frames) != 2 {
		t.Fatalf("frames = %v, want exactly [one four]", frames)
	}
	if frames[0] != "one" || frames[1] != "four" {
		t.Errorf("frames = %v, want [one four] (intermediates suppressed)", frames)
	}
}

// A poll iteration spanning wakeGapThreshold means the machine suspended:
// the client monitor must latch systemWoke and return so connectOnce tears
// the connection down for an immediate redial.
func TestMonitorLocalClipWakeGapLatchesAndReturns(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	stubClipboard(t, func() string { return "hello" })
	systemWoke.Store(false)

	base := time.Unix(1_700_000_000, 0)
	var calls atomic.Int64
	clockNow = func() time.Time {
		if calls.Add(1) == 1 {
			return base
		}
		return base.Add(60 * time.Second) // simulate a 60s suspend mid-poll
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		monitorLocalClip(bufio.NewWriter(io.Discard), nil, stop, true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not return after wake gap")
	}
	if !systemWoke.Load() {
		t.Error("systemWoke not latched after wake gap")
	}
	close(stop)
}

// The server path (checkWake=false) must never consult the clock — closing
// live server connections on resume would make healthy clients exit.
func TestMonitorLocalClipWithoutWakeCheckNeverReadsClock(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	stubClipboard(t, func() string { return "hello" })
	systemWoke.Store(false)
	var clockCalls atomic.Int64
	clockNow = func() time.Time {
		clockCalls.Add(1)
		return time.Now()
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		MonitorLocalClip(bufio.NewWriter(io.Discard), nil, stop)
		close(done)
	}()

	// Stay in the poll loop well past one iteration (would trip wake
	// detection if the clock were consulted with a jumping time).
	time.Sleep(1500 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("server monitor returned without stop being closed")
	default:
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server monitor did not return after stop")
	}
	if n := clockCalls.Load(); n != 0 {
		t.Errorf("clockNow called %d times without wake check, want 0", n)
	}
	if systemWoke.Load() {
		t.Error("systemWoke latched on server path")
	}
}

// Last client gone and nothing pending: after the grace period the server
// must exit(0) — via the stubbed exitProcess, not the real one.
func TestExitIfStillEmptyAfterExitsWhenEmpty(t *testing.T) {
	preserveGlobals(t)
	mu.Lock()
	listOfClients = nil
	activeConns = 0
	mu.Unlock()
	var mu2 sync.Mutex
	var codes []int
	exitProcess = func(code int) {
		mu2.Lock()
		codes = append(codes, code)
		mu2.Unlock()
	}

	exitIfStillEmptyAfter(50 * time.Millisecond)

	mu2.Lock()
	defer mu2.Unlock()
	if len(codes) != 1 || codes[0] != 0 {
		t.Errorf("exitProcess calls = %v, want [0]", codes)
	}
}

// A client that (re)connects during the grace window keeps the server alive.
func TestExitIfStillEmptyAfterStaysWhenClientPresent(t *testing.T) {
	preserveGlobals(t)
	mu.Lock()
	listOfClients = []*client{{w: bufio.NewWriter(io.Discard), addr: "peer:1"}}
	activeConns = 0
	mu.Unlock()
	var called atomic.Bool
	exitProcess = func(int) { called.Store(true) }

	exitIfStillEmptyAfter(50 * time.Millisecond)

	if called.Load() {
		t.Error("exitProcess called despite a connected client")
	}
}

// A connection mid-handshake (slot reserved, not yet listed) also aborts the
// exit — otherwise a waking peer's redial could be raced by the shutdown.
func TestExitIfStillEmptyAfterStaysWhileHandshakePending(t *testing.T) {
	preserveGlobals(t)
	mu.Lock()
	listOfClients = nil
	activeConns = 1
	mu.Unlock()
	var called atomic.Bool
	exitProcess = func(int) { called.Store(true) }

	exitIfStillEmptyAfter(50 * time.Millisecond)

	if called.Load() {
		t.Error("exitProcess called while a connection was pending")
	}
}

type muWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

// makeTestPNG encodes a solid-color PNG for image-payload tests.
func makeTestPNG(t *testing.T, r, g, b uint8) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		switch i % 4 {
		case 0:
			img.Pix[i] = r
		case 1:
			img.Pix[i] = g
		case 2:
			img.Pix[i] = b
		default:
			img.Pix[i] = 255
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture png: %v", err)
	}
	return buf.String()
}

func TestIsImagePayload(t *testing.T) {
	pngData := "\x89PNG\r\n\x1a\n" + "rest"
	jpegData := "\xff\xd8\xff\xe0" + "rest"
	bmpValid := func() string {
		b := make([]byte, 30)
		b[0], b[1] = 'B', 'M'
		binary.LittleEndian.PutUint32(b[14:18], 40)
		return string(b)
	}()
	cases := []struct {
		name, in string
		want     string
	}{
		{"png", pngData, "png"},
		{"jpeg", jpegData, "jpeg"},
		{"gif87", "GIF87a" + "rest", "gif"},
		{"gif89", "GIF89a" + "rest", "gif"},
		{"gif bad version", "GIF8xa" + "rest", ""},
		{"bmp valid header", bmpValid, "bmp"},
		{"bmp prose too short", "BM", ""},
		{"bmp prose wrong header", "BMW cars are fast and blue!!", ""},
		{"webp", "RIFF\x04\x00\x00\x00WEBPVP8 " + "rest", "webp"},
		{"riff not webp", "RIFF\x04\x00\x00\x00WAVEfmt ", ""},
		{"text", "hello world", ""},
		{"empty", "", ""},
		{"png-like text too short", "\x89PNG", ""},
	}
	for _, c := range cases {
		if got := imagePayloadFormat(c.in); got != c.want {
			t.Errorf("%s: format = %q, want %q", c.name, got, c.want)
		}
	}
}

// makeTestGIF encodes a small paletted GIF for image-payload tests.
func makeTestGIF(t *testing.T) string {
	t.Helper()
	pal := color.Palette{color.RGBA{10, 20, 30, 255}, color.RGBA{200, 10, 10, 255}}
	img := image.NewPaletted(image.Rect(0, 0, 8, 8), pal)
	for i := range img.Pix {
		img.Pix[i] = byte(i % 2)
	}
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture gif: %v", err)
	}
	return buf.String()
}

// testWebP1x1 is a known-valid 1×1 lossy WebP (base64).
const testWebP1x1 = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="

func TestImageFingerprintCrossFormatSamePixels(t *testing.T) {
	gifData := makeTestGIF(t)
	// Same pixels as PNG (decode the GIF, re-encode as PNG): fingerprints
	// must match so a GIF→PNG clipboard conversion is not an echo.
	img, _, err := image.Decode(strings.NewReader(gifData))
	if err != nil {
		t.Fatal(err)
	}
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	if imageFingerprint(gifData) != imageFingerprint(pngBuf.String()) {
		t.Error("same pixels in GIF vs PNG fingerprint mismatch (echo risk)")
	}
	// Different pixels must not collide.
	if imageFingerprint(gifData) == imageFingerprint(makeTestPNG(t, 200, 10, 10)) {
		t.Error("different images fingerprint-collide")
	}
	// Undecodable payloads keep the raw-byte fallback (BMP-like junk etc).
	junk := string([]byte{0x01, 0x02, 0x03, 0x04})
	if imageFingerprint(junk) == imageFingerprint(junk+"x") {
		t.Error("raw-byte fallback should distinguish payloads")
	}
}

func TestShrinkImageToFitGIF(t *testing.T) {
	// Small GIF: decode must be registered (x/image + image/gif imports) and
	// the ladder returns JPEG for anything it can fit.
	shrunk, err := shrinkImageToFit(makeTestGIF(t))
	if err != nil {
		t.Fatalf("shrink gif: %v", err)
	}
	if imagePayloadFormat(shrunk) != formatJPEG {
		t.Errorf("shrunk format = %q, want jpeg", imagePayloadFormat(shrunk))
	}
}

func TestWebpToPNG(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(testWebP1x1)
	if err != nil {
		t.Fatal(err)
	}
	pngBytes, err := webpToPNG(raw)
	if err != nil {
		t.Fatalf("webpToPNG: %v", err)
	}
	if imagePayloadFormat(string(pngBytes)) != formatPNG {
		t.Errorf("converted format = %q, want png", imagePayloadFormat(string(pngBytes)))
	}
	if _, err := webpToPNG([]byte("not webp")); err == nil {
		t.Error("expected error for non-webp input")
	}
}

func TestDarwinImageClass(t *testing.T) {
	cases := map[string][2]string{
		formatPNG:  {"PNGf", "png"},
		formatJPEG: {"JPEGf", "jpg"},
		formatGIF:  {"GIFf", "gif"},
		formatBMP:  {"BMPf", "bmp"},
		formatWebP: {"PNGf", "png"}, // webp is converted before class selection
		"":         {"PNGf", "png"},
	}
	for format, want := range cases {
		class, ext := darwinImageClass(format)
		if class != want[0] || ext != want[1] {
			t.Errorf("darwinImageClass(%q) = %q/%q, want %q/%q", format, class, ext, want[0], want[1])
		}
	}
}

func TestLinuxImageMIME(t *testing.T) {
	cases := map[string]string{
		formatPNG:  "image/png",
		formatJPEG: "image/jpeg",
		formatGIF:  "image/gif",
		formatBMP:  "image/bmp",
		formatWebP: "image/webp",
		"":         "image/png",
	}
	for format, want := range cases {
		if got := linuxImageMIME(format); got != want {
			t.Errorf("linuxImageMIME(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestParseClipboardInfoClasses(t *testing.T) {
	got, err := parseClipboardInfoClasses("{{«class GIFf», 64}, {«class PNGf», 4096}, {«class utxt», 300}}")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 2 || got[0] != "GIFf" || got[1] != "PNGf" {
		t.Errorf("parsed = %q, want [GIFf PNGf]", got)
	}
	// Real shapes macOS prints: space-padded BMP fourcc, plain labels, and
	// listed order preserved (raw type first, conversions after).
	got, err = parseClipboardInfoClasses("GIF picture, 42, «class PNGf», 173, «class BMP », 246, JPEG picture, 775, string, 27")
	if err != nil {
		t.Fatalf("parse real shape: %v", err)
	}
	want := []string{"GIFf", "PNGf", "BMP ", "JPEGf"}
	if len(got) != len(want) {
		t.Fatalf("parsed = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parsed[%d] = %q, want %q (full: %q)", i, got[i], want[i], got)
		}
	}
	if _, err := parseClipboardInfoClasses("{{«class utxt», 300}}"); err == nil {
		t.Error("expected error when no image classes listed")
	}
	if _, err := parseClipboardInfoClasses("garbage"); err == nil {
		t.Error("expected error for unparsable output")
	}
}

// makeTestBMP builds a minimal valid 1×1 24-bit BMP (54-byte header + padded
// pixel row) so BMP sniffing/decode/roundtrip tests have a real fixture.
func makeTestBMP(t *testing.T) string {
	t.Helper()
	b := make([]byte, 58)
	b[0], b[1] = 'B', 'M'
	binary.LittleEndian.PutUint32(b[2:], uint32(len(b))) //nolint:gosec // G115: fixture size, tiny
	binary.LittleEndian.PutUint32(b[10:], 54)
	binary.LittleEndian.PutUint32(b[14:], 40)
	binary.LittleEndian.PutUint32(b[18:], 1)
	binary.LittleEndian.PutUint32(b[22:], 1)
	binary.LittleEndian.PutUint16(b[26:], 1)
	binary.LittleEndian.PutUint16(b[28:], 24)
	b[54], b[55], b[56], b[57] = 10, 20, 30, 0
	return string(b)
}

func TestBMPFixtureDecodes(t *testing.T) {
	bmpData := makeTestBMP(t)
	if imagePayloadFormat(bmpData) != formatBMP {
		t.Fatalf("fixture format = %q, want bmp", imagePayloadFormat(bmpData))
	}
	if _, _, err := image.Decode(strings.NewReader(bmpData)); err != nil {
		t.Errorf("fixture does not decode (x/image/bmp registered?): %v", err)
	}
	// Fingerprint works through the pixel path now that BMP decodes.
	alt := makeTestBMP(t)
	altBytes := []byte(alt)
	altBytes[55] = 99 // different green channel → different pixels
	if imageFingerprint(alt) == imageFingerprint(string(altBytes)) {
		t.Error("BMP fingerprints should differ when pixel color changes")
	}
}

// TestLiveDarwinImageRoundtrip exercises the REAL macOS pasteboard: set each
// format through setDarwinImage, read it back through readDarwinImage, then
// restore whatever was there before (the run's clipboard changes may sync to
// a connected peer — this test is opt-in for that reason).
// Run with: CLIPPORT_LIVE_CLIPBOARD=1 go test -run TestLiveDarwinImageRoundtrip
func TestLiveDarwinImageRoundtrip(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("live pasteboard roundtrip is macOS-only")
	}
	if os.Getenv("CLIPPORT_LIVE_CLIPBOARD") == "" {
		t.Skip("set CLIPPORT_LIVE_CLIPBOARD=1 to run against the real pasteboard")
	}
	preserveGlobals(t)

	origText, pbErr := exec.Command("pbpaste").Output()
	if pbErr != nil {
		origText = nil
	}
	origImg, origImgErr := readLocalImage()
	restore := func() {
		if len(origText) > 0 {
			runSetClipCommand(string(origText))
		} else if origImgErr == nil {
			if err := writeImageClip(origImg); err != nil {
				t.Logf("restore original clipboard image: %v", err)
			}
		}
	}
	t.Cleanup(restore)

	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"gif", makeTestGIF(t), formatGIF},
		{"bmp", makeTestBMP(t), formatBMP},
		{"png", makeTestPNG(t, 10, 20, 30), formatPNG},
		{"webp converts to png", func() string {
			raw, err := base64.StdEncoding.DecodeString(testWebP1x1)
			if err != nil {
				t.Fatal(err)
			}
			return string(raw)
		}(), formatPNG},
	}
	for _, c := range cases {
		if err := setDarwinImage([]byte(c.payload)); err != nil {
			t.Fatalf("%s: set: %v", c.name, err)
		}
		back, err := readDarwinImage()
		if err != nil {
			t.Fatalf("%s: read back: %v", c.name, err)
		}
		if got := imagePayloadFormat(string(back)); got != c.want {
			t.Errorf("%s: roundtrip format = %q, want %q", c.name, got, c.want)
		}
		if c.want == formatGIF && string(back) != c.payload {
			t.Errorf("gif roundtrip bytes differ (%d in, %d out)", len(c.payload), len(back))
		}
	}
}

func TestClipboardStateChangedImageRoundtrip(t *testing.T) {
	original := makeTestPNG(t, 10, 20, 30)
	// Same pixels, re-encoded through decode→encode: fingerprint must match.
	img, _, err := image.Decode(strings.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	var reencoded bytes.Buffer
	if err := png.Encode(&reencoded, img); err != nil {
		t.Fatal(err)
	}
	if clipboardStateChanged(original, reencoded.String()) {
		t.Error("re-encoded identical image counted as a change (echo loop)")
	}
	different := makeTestPNG(t, 200, 10, 10)
	if !clipboardStateChanged(original, different) {
		t.Error("different image not detected as a change")
	}
	if clipboardStateChanged(original, original) {
		t.Error("identical bytes counted as a change")
	}
	if !clipboardStateChanged(original, "now text") {
		t.Error("image→text transition not detected")
	}
	if !clipboardStateChanged("text", original) {
		t.Error("text→image transition not detected")
	}
}

func TestParseOsascriptData(t *testing.T) {
	got, err := parseOsascriptData("«data PNGf89504E470D0A»\n", "PNGf")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !bytes.Equal(got, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A}) {
		t.Errorf("got %x, want 89504e470d0a", got)
	}
	if _, err := parseOsascriptData("garbage", "PNGf"); err == nil {
		t.Error("expected error on unexpected output")
	}
	if _, err := parseOsascriptData("«data PNGfzz»", "PNGf"); err == nil {
		t.Error("expected error on invalid hex")
	}
}

func TestMonitorLocalClipSendsImagePayload(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	pngData := makeTestPNG(t, 1, 2, 3)
	stubClipboard(t, func() string { return pngData })

	var mu sync.Mutex
	var wire bytes.Buffer
	w := bufio.NewWriter(&muWriter{mu: &mu, b: &wire})

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
			t.Fatal("MonitorLocalClip never wrote the image frame")
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
	if string(payload) != pngData {
		t.Error("image payload not sent verbatim")
	}
	if got := lastClipKindString(); got != clipKindImage {
		t.Errorf("lastClipKind = %q, want %q after image push", got, clipKindImage)
	}
	if got := lastClipBytes.Load(); got != int64(len(pngData)) {
		t.Errorf("lastClipBytes = %d, want %d", got, len(pngData))
	}
}

func TestMonitorSentClipsAppliesImagePayload(t *testing.T) {
	preserveGlobals(t)
	mu.Lock()
	listOfClients = nil
	mu.Unlock()
	pngData := makeTestPNG(t, 9, 9, 9)
	var setMu sync.Mutex
	var applied string
	getLocalClip = func() string { return "" }
	setLocalClip = func(s string) {
		setMu.Lock()
		applied = s
		setMu.Unlock()
	}

	data := encodeFrame(t, []byte(pngData))
	done := make(chan struct{})
	go func() {
		_ = MonitorSentClips(bufio.NewReader(bytes.NewReader(data)), nil)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("MonitorSentClips did not finish")
	}

	setMu.Lock()
	got := applied
	setMu.Unlock()
	if got != pngData {
		t.Error("image payload not applied to local clipboard")
	}
	mu.Lock()
	local := localClipboard
	mu.Unlock()
	if local != pngData {
		t.Error("localClipboard not updated with image payload")
	}
}

func TestRunSetClipCommandRoutesImageToWriter(t *testing.T) {
	preserveGlobals(t)
	var written atomic.Value
	writeImageClip = func(b []byte) error {
		written.Store(string(b))
		return nil
	}
	pngData := makeTestPNG(t, 7, 7, 7)
	runSetClipCommand(pngData)
	got, _ := written.Load().(string)
	if got != pngData {
		t.Error("writeImageClip did not receive image bytes")
	}
}

// oversizePNG builds (once) a PNG larger than maxClipboardFrameBytes by
// encoding high-entropy pixels (poorly compressible). Shared across tests —
// generation is expensive under -race.
var (
	oversizePNGOnce sync.Once
	oversizePNGData string
	oversizePNGErr  error
)

func oversizePNG(t *testing.T) string {
	t.Helper()
	oversizePNGOnce.Do(func() {
		const side = 2000
		img := image.NewRGBA(image.Rect(0, 0, side, side))
		seed := uint32(1)
		for i := 0; i < len(img.Pix); i += 4 {
			seed = seed*1664525 + 1013904223
			img.Pix[i] = byte(seed >> 24)
			img.Pix[i+1] = byte(seed >> 16)
			img.Pix[i+2] = byte(seed >> 8)
			img.Pix[i+3] = 255
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			oversizePNGErr = err
			return
		}
		if buf.Len() <= maxClipboardFrameBytes {
			oversizePNGErr = fmt.Errorf("fixture not oversize: %d <= %d", buf.Len(), maxClipboardFrameBytes)
			return
		}
		oversizePNGData = buf.String()
	})
	if oversizePNGErr != nil {
		t.Fatalf("oversize fixture: %v", oversizePNGErr)
	}
	return oversizePNGData
}

func TestShrinkImageToFit(t *testing.T) {
	oversize := oversizePNG(t)
	shrunk, err := shrinkImageToFit(oversize)
	if err != nil {
		t.Fatalf("shrink: %v", err)
	}
	if imagePayloadFormat(shrunk) != formatJPEG {
		t.Fatalf("shrunk format = %q, want jpeg", imagePayloadFormat(shrunk))
	}
	if len(shrunk) > maxClipboardFrameBytes-(64<<10) {
		t.Errorf("shrunk still too large: %d bytes", len(shrunk))
	}
	if _, err := shrinkImageToFit("not an image"); err == nil {
		t.Error("expected error for non-image payload")
	}
}

func TestSendFrameSkipsOversizeTextWithoutError(t *testing.T) {
	preserveGlobals(t)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	oversizeText := strings.Repeat("a", maxClipboardFrameBytes+1)
	if err := sendFrame(w, oversizeText, nil); err != nil {
		t.Fatalf("oversize text must skip, not error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no bytes on wire, got %d", buf.Len())
	}
	if !oversizeFrameReported.Load() {
		t.Error("skip latch not set")
	}
	if err := sendFrame(w, "small", nil); err != nil {
		t.Fatalf("send small: %v", err)
	}
	if oversizeFrameReported.Load() {
		t.Error("latch not cleared after successful send")
	}
}

func TestSendFrameShrinksOversizeImage(t *testing.T) {
	preserveGlobals(t)
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	oversize := oversizePNG(t)
	if err := sendFrame(w, oversize, nil); err != nil {
		t.Fatalf("oversize image send: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected shrunk frame on wire")
	}
	var payload []byte
	if err := gob.NewDecoder(bytes.NewReader(buf.Bytes())).Decode(&payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if imagePayloadFormat(string(payload)) != formatJPEG {
		t.Errorf("wire payload format = %q, want jpeg", imagePayloadFormat(string(payload)))
	}
}

func TestMonitorLocalClipSurvivesOversizeFrame(t *testing.T) {
	preserveGlobals(t)
	secondsBetweenChecksForClipChange = 1
	oversize := oversizePNG(t)
	var curMu sync.Mutex
	cur := oversize
	stubClipboard(t, func() string {
		curMu.Lock()
		defer curMu.Unlock()
		return cur
	})

	var mu sync.Mutex
	var wire bytes.Buffer
	w := bufio.NewWriter(&muWriter{mu: &mu, b: &wire})

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		MonitorLocalClip(w, nil, stop)
		close(done)
	}()

	// First frame: the oversize PNG, re-encoded under the cap (decode + JPEG
	// ladder on a slow runner can take a few seconds — allow 15).
	deadline := time.After(15 * time.Second)
	for {
		mu.Lock()
		n := wire.Len()
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("monitor never wrote a frame for oversize image")
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case <-done:
		t.Fatal("monitor exited after oversize frame")
	default:
	}

	mu.Lock()
	first := append([]byte(nil), wire.Bytes()...)
	mu.Unlock()
	var payload []byte
	if err := gob.NewDecoder(bytes.NewReader(first)).Decode(&payload); err != nil {
		t.Fatalf("decode first frame: %v", err)
	}
	if imagePayloadFormat(string(payload)) != formatJPEG {
		t.Errorf("first frame format = %q, want shrunk jpeg", imagePayloadFormat(string(payload)))
	}

	curMu.Lock()
	cur = "hello after oversize"
	curMu.Unlock()

	deadline = time.After(15 * time.Second)
	for {
		mu.Lock()
		n := wire.Len()
		mu.Unlock()
		if n > len(first) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("second frame never sent")
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case <-done:
		t.Fatal("monitor exited before stop")
	default:
	}

	close(stop)
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("monitor did not return after stop")
	}
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

// fakeProbeConn is a net.Conn double for stale-prune tests: Write either
// succeeds instantly or fails with writeErr, Close is recorded. Deadlines
// always arm so probeClient takes the real write path.
type fakeProbeConn struct {
	writeErr error
	writes   atomic.Int32
	closed   atomic.Bool
}

func (f *fakeProbeConn) Read([]byte) (int, error) { return 0, io.EOF }
func (f *fakeProbeConn) Write(b []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	f.writes.Add(1)
	return len(b), nil
}

// Close mirrors net.Conn: the first close succeeds, repeats error — the
// prune counter relies on this to count a dead peer only once.
func (f *fakeProbeConn) Close() error {
	if !f.closed.CompareAndSwap(false, true) {
		return errors.New("use of closed connection")
	}
	return nil
}
func (f *fakeProbeConn) LocalAddr() net.Addr         { return fakeAddr("local") }
func (f *fakeProbeConn) RemoteAddr() net.Addr        { return fakeAddr("remote") }
func (f *fakeProbeConn) SetDeadline(time.Time) error { return nil }
func (f *fakeProbeConn) SetReadDeadline(time.Time) error {
	return nil
}
func (f *fakeProbeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func TestPruneStaleClientsClosesWriteDeadPeer(t *testing.T) {
	preserveGlobals(t)
	dead := &fakeProbeConn{writeErr: errors.New("broken pipe")}
	mu.Lock()
	listOfClients = []*client{
		{w: bufio.NewWriter(dead), addr: "dead:1", conn: dead},
		{w: bufio.NewWriter(io.Discard), addr: "nilconn:1"},
	}
	mu.Unlock()

	pruneStaleClients()

	if !dead.closed.Load() {
		t.Error("write-dead peer was not closed")
	}
	if dead.writes.Load() != 0 {
		t.Errorf("dead peer recorded %d successful writes, want 0", dead.writes.Load())
	}
	if got := prunedClients.Load(); got != 1 {
		t.Errorf("prunedClients = %d, want 1 after closing a dead peer", got)
	}
	// A second pass before HandleClient cleanup unlists the conn must not
	// double-count: the repeat close errors, like net.Conn does.
	pruneStaleClients()
	if got := prunedClients.Load(); got != 1 {
		t.Errorf("prunedClients = %d after second pass, want still 1", got)
	}
}

func TestPruneStaleClientsKeepsLivePeer(t *testing.T) {
	preserveGlobals(t)
	live := &fakeProbeConn{}
	mu.Lock()
	listOfClients = []*client{{w: bufio.NewWriter(live), addr: "live:1", conn: live}}
	mu.Unlock()

	pruneStaleClients()

	if live.closed.Load() {
		t.Error("live peer must not be closed by pruning")
	}
	if live.writes.Load() == 0 {
		t.Error("live peer received no probe frame")
	}
	if got := prunedClients.Load(); got != 0 {
		t.Errorf("prunedClients = %d, want 0 when only live peers are probed", got)
	}
}

func TestPruneStaleClientsSkipsUnlistedClient(t *testing.T) {
	preserveGlobals(t)
	lonely := &fakeProbeConn{}
	mu.Lock()
	listOfClients = nil // entry already removed by HandleClient cleanup
	mu.Unlock()

	prune := probeClient(&client{w: bufio.NewWriter(lonely), addr: "gone:1", conn: lonely})

	if !prune {
		t.Error("unlisted client must be treated as already gone, not write-dead")
	}
	if lonely.writes.Load() != 0 || lonely.closed.Load() {
		t.Error("unlisted client must not be probed or closed")
	}
}

// A live HandleClient absorbs the empty probe frame: the peer stays listed,
// HandleClient keeps running, and the receiver drops the empty payload.
func TestPruneStaleClientsLeavesLiveHandleClientRunning(t *testing.T) {
	preserveGlobals(t)
	secure, keyMode = false, false
	stubClipboard(t, func() string { return "server-clip" })

	mu.Lock()
	listOfClients = nil
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

	// Wait for HandleClient to register the client.
	waitForRegisteredClient(t)

	pruneStaleClients()

	// Probe must not have torn the connection down, and the receiver must
	// see exactly one empty frame (it may also see the startup snapshot of
	// "server-clip" — skip non-empty frames).
	select {
	case <-hcDone:
		t.Fatal("HandleClient exited after probing a live peer")
	case <-time.After(200 * time.Millisecond):
	}
	if err := cli.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	waitForEmptyFrame(t, cli)

	mu.Lock()
	still := len(listOfClients) == 1 && listOfClients[0] != nil
	mu.Unlock()
	if !still {
		t.Error("live client was removed from the list by pruning")
	}

	// Join HandleClient before preserveGlobals restores stubbed globals.
	_ = cli.Close()
	select {
	case <-hcDone:
	case <-time.After(5 * time.Second):
		t.Fatal("HandleClient did not return after client disconnect")
	}
}

// waitForRegisteredClient waits until exactly one live client (with a conn)
// is listed — i.e. HandleClient finished registration and spawned monitors.
func waitForRegisteredClient(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		registered := len(listOfClients) == 1 && listOfClients[0] != nil && listOfClients[0].conn != nil
		mu.Unlock()
		if registered {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("HandleClient did not register the client")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForEmptyFrame reads gob frames from r until an empty payload arrives —
// the probe frame pruneStaleClients puts on the wire. Non-empty frames (the
// startup clipboard snapshot) are skipped. One decoder for the whole stream:
// a fresh gob.Decoder per frame drops bytes the previous decoder buffered.
func waitForEmptyFrame(t *testing.T, r io.Reader) {
	t.Helper()
	dec := gob.NewDecoder(r)
	for {
		var payload []byte
		if err := dec.Decode(&payload); err != nil {
			t.Fatalf("decoding probe/snapshot frame: %v", err)
		}
		if len(payload) == 0 {
			return
		}
	}
}

// The wake watcher detects a wall-clock gap (suspend/resume) and runs the
// prune hook once per wake.
func TestWatchServerWakeTriggersPrune(t *testing.T) {
	preserveGlobals(t)
	var prunes atomic.Int32
	pruneStale = func() { prunes.Add(1) }
	serverWakePoll = 5 * time.Millisecond
	serverWakeSettle = 5 * time.Millisecond
	wakeGapThreshold = time.Millisecond

	// First samples return T so the initial gap check is small; then jump an
	// hour ahead (suspend). After a prune the watcher re-anchors on the fake
	// future clock, so no further prunes fire before we stop it.
	base := time.Now()
	var samples atomic.Int32
	clockNow = func() time.Time {
		if samples.Add(1) <= 2 {
			return base
		}
		return base.Add(time.Hour)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		watchServerWake(stop)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for prunes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("watchServerWake never ran prune after a wake gap")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watchServerWake did not stop")
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
	if !strings.Contains(first, "cannot read clipboard:") {
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
	if !strings.Contains(third, "cannot read clipboard:") {
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
	if err := saveKnownPeers(path, map[string]knownPeer{
		"peer-a": {Key: "AAAA"},
		"peer-b": {Key: "BBBB"},
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
	if peers["peer-b"].Key != "BBBB" {
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
	oldDebounce := clipboardDebounce
	t.Cleanup(func() {
		secure, keyMode = oldSecure, oldKeyMode
		password = oldPassword
		listOfClients = oldClients
		localClipboard = oldClipboard
		getLocalClip, setLocalClip = oldGet, oldSet
		secondsBetweenChecksForClipChange = oldSeconds
		clipboardDebounce = oldDebounce
	})
}

// silence unused import if errors is only used in older tests

// waitForCondition polls cond until it returns true or the timeout elapses.
func waitForCondition(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestEndToEndLoopback runs makeServer and ConnectToServer in one process
// over a real loopback socket and walks the full path end to end: startup
// snapshot sync, clipboard change propagation (encrypted -s frames through
// the debounce window), clean EOF shutdown (the client exits instead of
// reconnecting), and the empty-server grace exit. Direction-specific
// behavior stays covered by the unit tests — in-process both sides share
// one clipboard, so this locks the plumbing down as a loop.
func TestEndToEndLoopback(t *testing.T) {
	preserveGlobals(t)
	setTestHome(t)
	secure, keyMode = true, false
	password = []byte("e2e-loopback-secret")
	secondsBetweenChecksForClipChange = 1
	clipboardDebounce = 50 * time.Millisecond
	emptyDisconnectGrace = 300 * time.Millisecond
	serverWakePoll, serverWakeSettle = 50*time.Millisecond, 50*time.Millisecond

	// Shared clipboard: getLocalClip exposes only the test-controlled value
	// (clipVal) — setLocalClip deliberately does NOT write it back, because
	// in-process both sides share this state: a late in-flight frame would
	// otherwise clobber a change the test just made. Wire applications are
	// observed through `applied` instead, so every recorded value still
	// proves real traffic crossed the loopback socket.
	var clipMu sync.Mutex
	clipVal := "initial-clip"
	var applied []string
	getLocalClip = func() string {
		clipMu.Lock()
		defer clipMu.Unlock()
		return clipVal
	}
	setLocalClip = func(s string) {
		clipMu.Lock()
		applied = append(applied, s)
		clipMu.Unlock()
	}
	getApplied := func() []string {
		clipMu.Lock()
		defer clipMu.Unlock()
		return append([]string(nil), applied...)
	}
	sawApplied := func(want string) func() bool {
		return func() bool {
			for _, s := range getApplied() {
				if s == want {
					return true
				}
			}
			return false
		}
	}

	var exitCode atomic.Int32
	exitCode.Store(-1)
	exitProcess = func(code int) { exitCode.Store(int32(code)) }

	// Reserve a free port, then start the server and wait for its listener
	// before dialing so the first connect attempt does not eat a backoff.
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	_ = ln.Close()

	srvDone := make(chan struct{})
	go func() {
		makeServer(port)
		close(srvDone)
	}()
	waitForCondition(t, 2*time.Second, "server listener to come up", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return runningServer != nil
	})

	cliDone := make(chan struct{})
	go func() {
		ConnectToServer(net.JoinHostPort("127.0.0.1", port))
		close(cliDone)
	}()
	waitForRegisteredClient(t)

	// 1. Startup sync: the first applied value means a frame crossed the wire.
	waitForCondition(t, 5*time.Second, "initial snapshot to sync to the client", func() bool {
		return len(getApplied()) > 0
	})

	// 2. Change propagation through debounce + encryption + monitors.
	clipMu.Lock()
	clipVal = "propagated-change"
	clipMu.Unlock()
	waitForCondition(t, 8*time.Second, "clipboard change to propagate", sawApplied("propagated-change"))

	// 3. FIN shutdown: closing the server-side conn delivers EOF; with -s the
	// client must take the clean-shutdown exit, not the reconnect path (an
	// unclean drop would back off ≥3s before redialing).
	mu.Lock()
	serverConn := listOfClients[0].conn
	mu.Unlock()
	_ = serverConn.Close()
	select {
	case <-cliDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not exit cleanly on server EOF")
	}

	// 4. Server side: last client gone → grace → exitProcess(0).
	waitForCondition(t, 3*time.Second, "server grace exit", func() bool {
		return exitCode.Load() == 0
	})

	// 5. HandleClient joined both monitors and removed its list entry — safe
	// for preserveGlobals to restore shared state now.
	waitForCondition(t, 3*time.Second, "server-side client cleanup", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(listOfClients) == 0
	})

	// 6. Stop the wake watcher (join it), then the accept loop, and join
	// makeServer.
	mu.Lock()
	h := runningServer
	mu.Unlock()
	if h == nil {
		t.Fatal("runningServer handle missing")
	}
	close(h.wakeStop)
	<-h.wakeDone
	_ = h.l.Close()
	select {
	case <-srvDone:
	case <-time.After(3 * time.Second):
		t.Fatal("makeServer did not stop after listener close")
	}
}
