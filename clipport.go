package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/term"

	"golang.org/x/crypto/scrypt"
)

var (
	secondsBetweenChecksForClipChange = 1
	helpMsg                           = `Clipport - Universal Clipboard
With Clipport, you can copy from one device and paste on another.

Usage: clipport [--port/-p] [--secure/-s] [--key/-k] [--debug/-d] [--quiet/-q] [--max-clients N] [ <address> | --help/-h ]
       clipport keygen
       clipport known-hosts [list|remove <peer>]
       clipport status
Examples:
   clipport                                   # start a new clipboard with randomized port
   clipport -p 6666                           # start a new clipboard on a set port number
   clipport -d                                # start a new clipboard with debug output
   clipport 192.168.86.24:53701               # join the clipboard at 192.168.86.24:53701
   clipport 192.168.86.24 -p 53701            # same as above, host and port given separately
   clipport [fe80::1]:53701                   # join via IPv6 (bracketed form; bare ::1 -p 53701 also works)
   clipport -d --secure 192.168.86.24:53701   # join the clipboard with debug output and enable encryption
    clipport keygen                            # generate a clipport keypair for use with --key
    clipport -k 192.168.86.24:53701            # join using keypair-based encryption instead of a password
    clipport known-hosts                       # list trusted -k peers
    clipport known-hosts remove 192.168.86.24  # forget a peer (after key rotation; peer IDs are host-only)
    clipport status                           # list connected clients of the running server
Running just ` + "`clipport`" + ` will start a new clipboard.
It will also provide an address with which you can connect to the same clipboard with another device.
With --secure, the password is read from the CLIPPORT_SECRET environment variable if set,
otherwise you'll be prompted for it. Set CLIPPORT_SECRET on both machines to skip the prompt on both ends.
With --key, each device uses its own keypair (run ` + "`clipport keygen`" + ` once per device) and no
secret ever has to be typed or shared; the first connection to a given peer trusts its public key and
remembers it under ~/.clipport/known_peers, warning loudly if that peer's key ever changes later.
Connecting without --secure or --key will prompt for confirmation since the clipboard is sent in plaintext.
State (keys, known_peers) lives in ~/.clipport; override with the CLIPPORT_DIR env var or --dir.
With --quiet/-q, status chatter is suppressed (errors and interactive prompts still print) — suited to launchd/systemd.
--max-clients N caps how many peers the server accepts at once (default 8, 0 = unlimited); extras are rejected and logged.
Refer to https://github.com/tsyche/clipport for more information`
	mu             sync.Mutex
	peersMu        sync.Mutex // guards read-modify-write of known_peers
	listOfClients  = make([]*client, 0)
	localClipboard string
	printDebugInfo = false
	quiet          = false
	// maxClients caps concurrent server connections (0 = unlimited); counts
	// pending handshakes too via activeConns, not just listed clients.
	maxClients = 8
	// activeConns counts accepted-but-not-yet-finished connections (slot
	// accounting for --max-clients). Guarded by mu.
	activeConns = 0
	// clipboardDebounce is the quiet window MonitorLocalClip waits after an
	// observed change before sending — coalesces rapid multi-step edits into
	// one frame carrying the final value. Package var so tests can shorten it.
	clipboardDebounce = 250 * time.Millisecond
	version           = "dev"
	cryptoStrength    = 16384
	secure            = false
	keyMode           = false
	password          []byte

	// stateDir overrides where keys/known_peers live (--dir flag; empty means
	// fall back to $CLIPPORT_DIR, then ~/.clipport — see clipportDir).
	stateDir = ""

	// Clipboard access is routed through vars so tests can stub the system
	// clipboard without depending on pbpaste/xclip being present or writable.
	getLocalClip = runGetClipCommand
	setLocalClip = runSetClipCommand

	// clipReadErrReported latches after the first clipboard read failure so
	// non-text content (image/file) does not spam handleError every poll
	// (uniclip#23). Cleared when a read succeeds.
	clipReadErrReported atomic.Bool

	// wakeGapThreshold: a MonitorLocalClip poll iteration that takes at least
	// this long means the machine suspended mid-poll (sleep/wake). The client
	// then tears the connection down instead of waiting minutes for TCP
	// keepalive probes to time out. Package var so tests can reason about it.
	wakeGapThreshold = 30 * time.Second

	// systemWoke latches when a wake gap was detected; ConnectToServer reads
	// and clears it to skip reconnect backoff after resume.
	systemWoke atomic.Bool

	// clockNow is time.Now; tests stub it to fake suspend/resume gaps.
	clockNow = time.Now

	// emptyDisconnectGrace is how long the server waits after the last client
	// disconnects before exiting. A waking client closes its stale connection
	// and redials immediately; without the grace window the server would see
	// an empty client list for that sub-second gap and exit first, leaving
	// the client with nobody to reconnect to.
	emptyDisconnectGrace = 10 * time.Second

	// exitProcess is os.Exit; tests stub it to observe shutdown without
	// killing the test binary.
	exitProcess = os.Exit
)

// maxClipboardFrameBytes caps a single gob-encoded clipboard frame on the
// wire. Guards MonitorSentClips against a malicious or buggy peer sending
// an unbounded payload and exhausting memory before decode finishes.
const maxClipboardFrameBytes = 8 << 20 // 8 MiB

var errClipboardTooLarge = errors.New("clipboard frame exceeds size limit")

// permanentError marks a failure that cannot succeed by retrying (e.g. a -k
// peer key mismatch). ConnectToServer stops instead of looping forever.
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

func isPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

// client pairs a connected peer's writer with the encryption key negotiated
// for that specific connection (nil if unencrypted).
type client struct {
	w    *bufio.Writer
	key  []byte
	addr string
}

func main() { //nolint:gocyclo // flag parsing + dispatch; branch count is inherent to the CLI surface, not a complexity smell
	var (
		port        string
		showVersion bool
	)

	flag.StringVar(&port, "p", "", "Specify the port to listen on")
	flag.StringVar(&port, "port", "", "Specify the port to listen on")
	flag.StringVar(&stateDir, "dir", "", "State directory for keys and known_peers (default ~/.clipport; also CLIPPORT_DIR)")
	flag.BoolVar(&secure, "s", false, "Encrypt your data using a shared password")
	flag.BoolVar(&secure, "secure", false, "Encrypt your data using a shared password")
	flag.BoolVar(&keyMode, "k", false, "Encrypt your data using a clipport keypair (see `clipport keygen`)")
	flag.BoolVar(&keyMode, "key", false, "Encrypt your data using a clipport keypair (see `clipport keygen`)")
	flag.BoolVar(&printDebugInfo, "d", false, "Enable debug output")
	flag.BoolVar(&printDebugInfo, "debug", false, "Enable debug output")
	flag.BoolVar(&quiet, "q", false, "Quiet mode: print errors and prompts only (for headless/launchd use)")
	flag.BoolVar(&quiet, "quiet", false, "Quiet mode: print errors and prompts only (for headless/launchd use)")
	flag.IntVar(&maxClients, "max-clients", 8, "Max concurrent clients on the server (0 = unlimited)")
	flag.BoolVar(&showVersion, "v", false, "Print version")
	flag.BoolVar(&showVersion, "version", false, "Print version")
	flag.Usage = func() { fmt.Println(helpMsg) }

	flag.Parse()

	if showVersion {
		fmt.Println(version)
		return
	}

	args := flag.Args()
	if len(args) == 1 && args[0] == "keygen" {
		runKeygen()
		return
	}
	if len(args) >= 1 && args[0] == "known-hosts" {
		if err := runKnownHosts(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if len(args) == 1 && args[0] == "status" {
		runStatus()
		return
	}
	if len(args) > 1 {
		handleError(errors.New("too many arguments"))
		fmt.Println(helpMsg)
		return
	}

	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			fmt.Fprintln(os.Stderr, "error: invalid port number:", port)
			os.Exit(1)
		}
	}

	if maxClients < 0 {
		fmt.Fprintln(os.Stderr, "error: --max-clients must be >= 0 (0 = unlimited)")
		os.Exit(1)
	}

	if secure && keyMode {
		fmt.Fprintln(os.Stderr, "error: use either -s (password) or -k (keypair), not both")
		os.Exit(1)
	}
	if keyMode {
		secure = true
	}

	if !secure {
		if !confirmPlaintext() {
			fmt.Println("Aborted.")
			return
		}
	} else if !keyMode {
		password = resolvePassword()
	}

	if len(args) == 1 {
		address, err := resolveClientAddress(args[0], port)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		ConnectToServer(address)
		return
	}

	makeServer(port)
}

// resolvePassword reads the secure-mode password from CLIPPORT_SECRET if set,
// otherwise prompts for it interactively.
func resolvePassword() []byte {
	if v := os.Getenv("CLIPPORT_SECRET"); v != "" {
		return []byte(v)
	}
	fmt.Print("Password for --secure: ")
	pw, err := term.ReadPassword(int(syscall.Stdin)) //nolint:unconvert // syscall.Stdin's underlying type differs across the cross-compiled GOOS targets; the cast is a no-op on linux but required elsewhere
	fmt.Println()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: could not read password:", err)
		os.Exit(1)
	}
	return pw
}

// resolveClientAddress combines a host (optionally with an embedded port) and
// an optional -p port into a single dialable address, erroring if both are
// given but disagree. IPv6 works in every common form: bare ("::1"), bracketed
// ("[::1]"), and bracketed with a port ("[::1]:53701"); output is always
// bracket-correct via net.JoinHostPort.
func resolveClientAddress(addr, port string) (string, error) {
	host, embeddedPort, err := net.SplitHostPort(addr)
	if err != nil {
		if port == "" {
			return "", errors.New("no port specified: use host:port or -p")
		}
		// SplitHostPort failed: no embedded port. Accept bracketed bare
		// IPv6 ("[::1]") as well as plain hosts before joining.
		bare := strings.TrimSuffix(strings.TrimPrefix(addr, "["), "]")
		return net.JoinHostPort(bare, port), nil
	}
	if port != "" && port != embeddedPort {
		return "", fmt.Errorf("conflicting ports: %s in address vs -p %s", embeddedPort, port)
	}
	return net.JoinHostPort(host, embeddedPort), nil
}

// confirmPlaintext warns the user that no encryption was requested and asks
// for confirmation before continuing.
func confirmPlaintext() bool {
	fmt.Print("Warning: no encryption requested (-s or -k). Clipboard contents will be sent in plaintext. Continue? [y/N] ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "y" || line == "yes"
}

// resolveConnectionKey determines the encryption key for a single connection:
// nil for plaintext, the shared password in -s mode, or an ECDH-derived
// secret unique to this peer in -k mode.
func resolveConnectionKey(c net.Conn, isServer bool, dialedAddress string) ([]byte, error) {
	if !secure {
		return nil, nil
	}
	if !keyMode {
		return password, nil
	}

	priv, err := loadKeypair()
	if err != nil {
		return nil, err
	}
	pubBytes := priv.PublicKey().Bytes()

	if _, err := c.Write(pubBytes); err != nil {
		return nil, err
	}
	peerBytes := make([]byte, 32)
	if _, err := io.ReadFull(c, peerBytes); err != nil {
		return nil, err
	}
	peerPub, err := ecdh.X25519().NewPublicKey(peerBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid peer public key: %w", err)
	}

	var peerID string
	if isServer {
		peerID, _, err = net.SplitHostPort(c.RemoteAddr().String())
	} else {
		peerID, _, err = net.SplitHostPort(dialedAddress)
	}
	if err != nil {
		peerID = c.RemoteAddr().String()
	}

	localErr := verifyOrTrustPeer(peerID, peerBytes)
	status := byte(1)
	if localErr != nil {
		status = 0
	}
	if _, err := c.Write([]byte{status}); err != nil {
		return nil, err
	}
	peerStatus := make([]byte, 1)
	if _, err := io.ReadFull(c, peerStatus); err != nil {
		return nil, err
	}
	if localErr != nil {
		return nil, permanent(localErr)
	}
	if peerStatus[0] == 0 {
		return nil, permanent(errors.New("peer rejected the connection (its key verification failed on its end)"))
	}

	return priv.ECDH(peerPub)
}

// clipportDir returns the state directory (keys, known_peers): --dir if given,
// else $CLIPPORT_DIR, else ~/.clipport. Created if necessary.
func clipportDir() (string, error) {
	dir := stateDir
	if dir == "" {
		dir = os.Getenv("CLIPPORT_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".clipport")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// runKeygen generates a new X25519 keypair for --key mode and stores it
// in the state directory (clipportDir), refusing to overwrite an existing key.
func runKeygen() {
	dir, err := clipportDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	keyPath, pub, err := generateKeypair(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("Generated clipport key at", keyPath)
	fmt.Println("Fingerprint:", fingerprint(pub))
}

// generateKeypair writes a fresh X25519 keypair into dir, refusing to
// overwrite an existing key. Returns the private key path and public key bytes.
func generateKeypair(dir string) (string, []byte, error) {
	keyPath := filepath.Join(dir, "key")
	if _, err := os.Stat(keyPath); err == nil {
		return "", nil, fmt.Errorf("a key already exists at %s, remove it first to regenerate", keyPath)
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	pub := priv.PublicKey().Bytes()
	if err := os.WriteFile(keyPath, priv.Bytes(), 0600); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pub"), pub, 0600); err != nil {
		return "", nil, err
	}
	return keyPath, pub, nil
}

// loadKeypair reads the device's clipport keypair, generated by `clipport keygen`.
func loadKeypair() (*ecdh.PrivateKey, error) {
	dir, err := clipportDir()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "key"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("no clipport key found, run `clipport keygen` first")
		}
		return nil, err
	}
	return ecdh.X25519().NewPrivateKey(raw)
}

// verifyOrTrustPeer implements trust-on-first-connect: the first time a peer
// ID is seen, its public key is recorded; on later connections, a changed
// key aborts loudly instead of silently proceeding.
func verifyOrTrustPeer(peerID string, pubKey []byte) error {
	peersMu.Lock()
	defer peersMu.Unlock()
	dir, err := clipportDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "known_peers")
	peers, err := loadKnownPeers(path)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(pubKey)
	if existing, ok := peers[peerID]; ok {
		if existing != encoded {
			return fmt.Errorf("WARNING: public key for %s has changed since the last connection.\n"+
				"This could mean someone is impersonating that peer, or it legitimately regenerated its key.\n"+
				"If this is expected, run `clipport known-hosts remove %s` and reconnect (or edit %s by hand)",
				peerID, peerID, path)
		}
		return nil
	}
	peers[peerID] = encoded
	if err := saveKnownPeers(path, peers); err != nil {
		return err
	}
	fmt.Printf("Trusting new peer %s (fingerprint %s)\n", peerID, fingerprint(pubKey))
	return nil
}

func loadKnownPeers(path string) (map[string]string, error) {
	peers := make(map[string]string)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return peers, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		peers[parts[0]] = parts[1]
	}
	return peers, nil
}

func saveKnownPeers(path string, peers map[string]string) error {
	var b strings.Builder
	for id, key := range peers {
		fmt.Fprintf(&b, "%s %s\n", id, key)
	}
	return os.WriteFile(path, []byte(b.String()), 0600)
}

// removeKnownPeer deletes id from the known_peers file at path.
func removeKnownPeer(path, id string) error {
	peers, err := loadKnownPeers(path)
	if err != nil {
		return err
	}
	if _, ok := peers[id]; !ok {
		return fmt.Errorf("no known peer %q in %s", id, path)
	}
	delete(peers, id)
	return saveKnownPeers(path, peers)
}

// runKnownHosts implements `clipport known-hosts [list|remove <peer>]`.
func runKnownHosts(args []string) error {
	dir, err := clipportDir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "known_peers")
	peersMu.Lock()
	defer peersMu.Unlock()

	cmd := "list"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}
	switch cmd {
	case "list":
		if len(args) != 0 {
			return errors.New("usage: clipport known-hosts [list|remove <peer>]")
		}
		return listKnownPeers(path)
	case "remove":
		if len(args) != 1 {
			return errors.New("usage: clipport known-hosts remove <peer>")
		}
		if err := removeKnownPeer(path, args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed peer %s\n", args[0])
		return nil
	default:
		return fmt.Errorf("unknown known-hosts command %q (want list or remove)", cmd)
	}
}

func listKnownPeers(path string) error {
	peers, err := loadKnownPeers(path)
	if err != nil {
		return err
	}
	if len(peers) == 0 {
		fmt.Printf("No known peers in %s\n", path)
		return nil
	}
	ids := make([]string, 0, len(peers))
	for id := range peers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		pub, err := base64.StdEncoding.DecodeString(peers[id])
		if err != nil {
			fmt.Printf("%s  (unreadable key)\n", id)
			continue
		}
		fmt.Printf("%s  fingerprint %s\n", id, fingerprint(pub))
	}
	return nil
}

func fingerprint(pubKey []byte) string {
	sum := sha256.Sum256(pubKey)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// lastClipPush records when MonitorLocalClip last pushed a non-empty clipboard
// frame to a peer (unix nanos; 0 = never). Reported by `clipport status`.
var lastClipPush atomic.Int64

// statusSnapshot is the JSON payload `clipport status` reads from the local
// status socket. Clients holds connected peers' remote addresses.
type statusSnapshot struct {
	Pid          int      `json:"pid"`
	Port         string   `json:"port"`
	Clients      []string `json:"clients"`
	MaxClients   int      `json:"max_clients"`    // 0 = unlimited
	LastClipPush int64    `json:"last_clip_push"` // unix nanos; 0 = never
}

func statusSocketPath(dir string) string {
	return filepath.Join(dir, "clipport.sock")
}

// currentStatus builds a snapshot of live server state under the same mutex
// that guards listOfClients.
func currentStatus(port string) statusSnapshot {
	mu.Lock()
	clients := make([]string, 0, len(listOfClients))
	for _, c := range listOfClients {
		if c != nil {
			clients = append(clients, c.addr)
		}
	}
	max := maxClients
	mu.Unlock()
	return statusSnapshot{
		Pid:          os.Getpid(),
		Port:         port,
		Clients:      clients,
		MaxClients:   max,
		LastClipPush: lastClipPush.Load(),
	}
}

// serveStatus writes a status JSON line to every connection on l, then closes
// it. Runs until the listener fails (process exit closes it implicitly).
func serveStatus(l net.Listener, port string) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		data, err := json.Marshal(currentStatus(port))
		if err == nil {
			_, _ = c.Write(append(data, '\n')) //nolint:errcheck // best-effort status reply; a failed write just loses the report
		}
		_ = c.Close()
	}
}

// startStatusServer opens the local unix-socket status endpoint in the state
// directory. Best-effort: if the platform or directory cannot support it, the
// server runs without `clipport status`. A stale socket from a crashed server
// is unlinked first; if two servers share a state dir, the newer one owns it.
func startStatusServer(port string) {
	dir, err := clipportDir()
	if err != nil {
		debug("status socket: no state dir:", err)
		return
	}
	sock := statusSocketPath(dir)
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		debug("status socket unavailable:", err)
		return
	}
	_ = os.Chmod(sock, 0600) //nolint:errcheck // best-effort hardening; socket still works if chmod fails
	go serveStatus(l, port)
}

// queryStatus dials the local status socket and reads one status line.
func queryStatus(dir string) (statusSnapshot, error) {
	conn, err := net.Dial("unix", statusSocketPath(dir))
	if err != nil {
		return statusSnapshot{}, fmt.Errorf("no running clipport server (status socket %s): %w", statusSocketPath(dir), err)
	}
	defer conn.Close()
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return statusSnapshot{}, fmt.Errorf("reading status: %w", err)
	}
	var st statusSnapshot
	if err := json.Unmarshal(line, &st); err != nil {
		return statusSnapshot{}, fmt.Errorf("decoding status: %w", err)
	}
	return st, nil
}

// runStatus implements `clipport status`: report the running server's pid,
// port, connected clients, and last clipboard push. Always prints (direct
// subcommand output, not gated by --quiet); exits 1 when no server answers.
func runStatus() {
	dir, err := clipportDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	st, err := queryStatus(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Printf("clipport server running (pid %d, port %s)\n", st.Pid, st.Port)
	switch {
	case len(st.Clients) == 0:
		fmt.Println("Clients (0): none connected")
	case st.MaxClients > 0:
		fmt.Printf("Clients (%d/%d):\n", len(st.Clients), st.MaxClients)
	default:
		fmt.Printf("Clients (%d):\n", len(st.Clients))
	}
	for _, addr := range st.Clients {
		fmt.Printf("  %s\n", addr)
	}
	if st.LastClipPush == 0 {
		fmt.Println("Clipboard last pushed: never")
	} else {
		ago := time.Since(time.Unix(0, st.LastClipPush)).Round(time.Second)
		fmt.Printf("Clipboard last pushed: %s ago\n", ago)
	}
}

// tryReserveClientSlot accounts one more accepted connection against
// --max-clients, returning false when the server is full (maxClients 0 =
// unlimited). Pairs with releaseClientSlot when the connection finishes.
func tryReserveClientSlot() bool {
	mu.Lock()
	defer mu.Unlock()
	if maxClients > 0 && activeConns >= maxClients {
		return false
	}
	activeConns++
	return true
}

func releaseClientSlot() {
	mu.Lock()
	activeConns--
	mu.Unlock()
}

func makeServer(port string) {
	info("Starting a new clipboard")
	listenAddr := ":"
	if port != "" {
		listenAddr = ":" + port
	}
	l, err := net.Listen("tcp", listenAddr) //nolint // dual-stack: binds IPv4 and IPv6 where the OS allows it
	if err != nil {
		handleError(err)
		return
	}
	defer l.Close()
	if port == "" {
		port = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	}
	startStatusServer(port)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		info("\nShutting down — connected devices will be notified.")
		os.Exit(0)
	}()
	info("Run", "`clipport", net.JoinHostPort(getOutboundIP().String(), port)+"`", "to join this clipboard")
	info()
	for {
		c, err := l.Accept()
		if err != nil {
			handleError(err)
			return
		}
		if !tryReserveClientSlot() {
			infof("Rejecting %s: server full (--max-clients %d)\n", c.RemoteAddr(), maxClients)
			_ = c.Close()
			continue
		}
		enableKeepAlive(c)
		info("Connected to device at " + c.RemoteAddr().String())
		go func() {
			defer releaseClientSlot()
			HandleClient(c)
		}()
	}
}

// enableKeepAlive turns on OS-level TCP keepalive probes so that idle
// connections dropped by NAT/firewall timeouts are detected instead of
// silently hanging until the next write.
func enableKeepAlive(c net.Conn) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetKeepAlive(true)                   //nolint:errcheck // best-effort; a failure here just means slower dead-peer detection, not a functional break
	_ = tc.SetKeepAlivePeriod(30 * time.Second) //nolint:errcheck // best-effort, see above
}

// Handle a client as a server
func HandleClient(c net.Conn) {
	addr := c.RemoteAddr().String()
	defer c.Close()
	key, err := resolveConnectionKey(c, true, "")
	if err != nil {
		handleError(err)
		return
	}
	w := bufio.NewWriter(c)
	cl := &client{w: w, key: key, addr: c.RemoteAddr().String()}
	mu.Lock()
	listOfClients = append(listOfClients, cl)
	mu.Unlock()

	// Detecting a dead peer can come from either side: a read error on the
	// client's sent clips (e.g. keepalive finally timing out), or a write
	// error next time the local clipboard changes. Whichever happens first
	// should trigger cleanup — don't block only on the write side, since the
	// local clipboard may never change again.
	finished := make(chan struct{}, 2)
	stopLocal := make(chan struct{})
	go func() {
		MonitorSentClips(bufio.NewReader(c), key)
		finished <- struct{}{}
	}()
	go func() {
		MonitorLocalClip(w, key, stopLocal)
		finished <- struct{}{}
	}()
	<-finished
	close(stopLocal)
	_ = c.Close() // unblock whichever goroutine is still running
	<-finished    // both monitors must exit before we touch shared state / return

	info("Lost connection from", addr)
	mu.Lock()
	newClients := make([]*client, 0, len(listOfClients))
	for _, existing := range listOfClients {
		if existing != nil && existing != cl {
			newClients = append(newClients, existing)
		}
	}
	listOfClients = newClients
	noClients := len(listOfClients) == 0
	mu.Unlock()
	if noClients {
		// Grace, not instant exit: a peer that just woke from sleep (or hit
		// a transient blip) closes its stale connection and redials right
		// away — exiting here would race that redial and orphan the peer.
		go exitIfStillEmptyAfter(emptyDisconnectGrace)
	}
}

// exitIfStillEmptyAfter waits for the disconnect grace period, then shuts the
// process down if no client has (re)connected in the meantime. Skips the exit
// while a client is listed or another connection is mid-handshake.
func exitIfStillEmptyAfter(d time.Duration) {
	time.Sleep(d)
	mu.Lock()
	stillEmpty := len(listOfClients) == 0 && activeConns == 0
	mu.Unlock()
	if !stillEmpty {
		debug("client reconnected during disconnect grace; staying alive")
		return
	}
	info("All devices disconnected. Exiting.")
	exitProcess(0)
}

// Connect to the server (which starts a new clipboard), reconnecting
// automatically if the connection drops while the server is still up.
func ConnectToServer(address string) {
	const (
		baseBackoff = 3 * time.Second
		maxBackoff  = 30 * time.Second
	)
	backoff := baseBackoff
	for {
		retry, reached := connectOnce(address)
		if !retry {
			return
		}
		if reached {
			// A successful session means the last failure mode is over —
			// start the next outage at the short delay again.
			backoff = baseBackoff
		}
		if systemWoke.Swap(false) {
			// Resume from sleep: the old connection was torn down by wake
			// detection, so redial now instead of honoring stale backoff.
			info("System resume detected; reconnecting immediately")
			continue
		}
		time.Sleep(backoff)
		backoff = nextBackoff(backoff, maxBackoff)
	}
}

// nextBackoff doubles cur, capped at max.
func nextBackoff(cur, max time.Duration) time.Duration {
	n := cur * 2
	if n > max {
		return max
	}
	return n
}

// connectOnce dials the server and runs the clipboard sync until either
// direction of the connection fails. Returns (retry, reached): retry is false
// when the caller should give up (clean shutdown, plaintext drop, or a
// permanent -k key mismatch); reached is true if a session was established
// (used to reset reconnect backoff).
func connectOnce(address string) (retry, reached bool) {
	c, err := net.Dial("tcp", address) // dual-stack: resolver tries IPv6 and IPv4 addresses
	if c == nil {
		handleError(err)
		info("Could not connect to", address)
		return true, false
	}
	if err != nil {
		handleError(err)
		return true, false
	}
	enableKeepAlive(c)

	key, err := resolveConnectionKey(c, false, address)
	if err != nil {
		_ = c.Close()
		handleError(err)
		if isPermanent(err) {
			fmt.Println("Permanent failure; not reconnecting.")
			fmt.Println("If you rotated this peer's key, run `clipport known-hosts remove <peer>` (see warning above), then try again.")
			return false, false
		}
		return true, false
	}
	infof("Connected to the clipboard at %s\n", address)

	cleanShutdown := false
	done := make(chan struct{})
	stopLocal := make(chan struct{})
	go func() {
		if MonitorSentClips(bufio.NewReader(c), key) {
			cleanShutdown = true
		}
		// Always release the local monitor — on both clean EOF and unclean
		// drop, otherwise MonitorLocalClip can block forever waiting for a
		// clipboard change that never comes.
		close(stopLocal)
		_ = c.Close()
		close(done)
	}()
	monitorLocalClip(bufio.NewWriter(c), key, stopLocal, true)
	_ = c.Close()
	<-done
	if cleanShutdown {
		infof("Server at %s shut down. Exiting.\n", address)
		return false, true
	}
	if key != nil {
		infof("Connection to %s lost. Reconnecting...\n", address)
		return true, true
	}
	fmt.Printf("Connection to %s lost (unencrypted). Reconnecting without encryption is unsafe.\n", address)
	fmt.Println("Use -k or -s for secure reconnections. Exiting.")
	return false, true
}

// MonitorLocalClip is the server-safe wrapper: same as monitorLocalClip but
// never wake-detects. A server that closes live connections on resume would
// make healthy clients see EOF, which they mistake for a clean server
// shutdown and exit permanently.
func MonitorLocalClip(w *bufio.Writer, key []byte, stop <-chan struct{}) {
	monitorLocalClip(w, key, stop, false)
}

// monitors for changes to the local clipboard and writes them to w.
// Returns when stop is closed, the write fails, or the connection drops.
// Empty frames are not put on the wire: getLocalClip returns "" for a
// cleared clipboard, at startup, and whenever the OS clipboard has no
// text representation (e.g. macOS pbpaste on an image). Sending those
// would wipe peers; see MonitorSentClips for the receive-side backstop.
// The initial snapshot sends immediately; subsequent changes pass through
// a quiet-window debounce so a burst of edits puts one frame (the final
// value) on the wire instead of one per poll.
// When checkWake is set (client connections only), a poll iteration that
// takes wakeGapThreshold or longer is treated as a suspend/resume: systemWoke
// is latched and the monitor returns, which tears the connection down so the
// reconnect loop can redial immediately instead of waiting out TCP keepalive.
func monitorLocalClip(w *bufio.Writer, key []byte, stop <-chan struct{}, checkWake bool) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		mu.Lock()
		localClipboard = getLocalClip()
		cur := localClipboard
		mu.Unlock()
		debug("localClipboard changed. localClipboard =", cur)
		// localClipboard is already updated above even when cur is empty, so
		// the change-poll below does not spin while the clipboard stays empty.
		if cur != "" {
			err := sendClipboard(w, cur, key)
			if err != nil {
				if !isNetworkDisconnect(err) {
					handleError(err)
				}
				return
			}
			lastClipPush.Store(time.Now().UnixNano())
		}
		for {
			var iterStart time.Time
			if checkWake {
				iterStart = clockNow()
			}
			select {
			case <-stop:
				return
			case <-time.After(time.Second * time.Duration(secondsBetweenChecksForClipChange)):
			}
			if checkWake {
				if gap := clockNow().Sub(iterStart); gap >= wakeGapThreshold {
					debug("system resume detected (poll gap", gap, "); dropping connection")
					systemWoke.Store(true)
					return
				}
			}
			// Re-read localClipboard under mu: MonitorSentClips writes it
			// concurrently when a remote peer updates the clipboard.
			mu.Lock()
			last := localClipboard
			mu.Unlock()
			if last != getLocalClip() {
				break
			}
		}
		// Change observed: coalesce until the value holds still for the
		// debounce window, then let the top of the loop send the latest.
		if !waitClipboardQuiet(stop) {
			return
		}
	}
}

// waitClipboardQuiet polls getLocalClip until the value has been unchanged
// for clipboardDebounce (resetting the window on every new value). Returns
// false if stop closed first.
func waitClipboardQuiet(stop <-chan struct{}) bool {
	const poll = 50 * time.Millisecond
	lastChange := time.Now()
	latest := getLocalClip()
	for time.Since(lastChange) < clipboardDebounce {
		select {
		case <-stop:
			return false
		case <-time.After(poll):
		}
		if cur := getLocalClip(); cur != latest {
			latest = cur
			lastChange = time.Now()
		}
	}
	return true
}

// monitors for clipboards sent through r; returns true on clean server shutdown (EOF)
func MonitorSentClips(r *bufio.Reader, key []byte) bool {
	var foreignClipboard string
	var foreignClipboardBytes []byte
	// One decoder for the stream: a fresh gob.Decoder each loop drops bytes the
	// previous decoder already buffered, silently losing subsequent frames.
	// N is reset before each Decode so the size cap applies per frame.
	lr := &io.LimitedReader{R: r, N: maxClipboardFrameBytes + 1}
	dec := gob.NewDecoder(lr)
	for {
		lr.N = maxClipboardFrameBytes + 1
		err := dec.Decode(&foreignClipboardBytes)
		if lr.N <= 0 {
			// Hit the cap mid-message (or frame was larger than max): stream
			// is desynced and peer is misbehaving — disconnect, do not continue.
			fmt.Fprintf(os.Stderr, "error: peer sent clipboard frame larger than %d bytes; disconnecting\n", maxClipboardFrameBytes)
			return false
		}
		if err != nil {
			// Clean EOF = server shutdown. Any other error (unexpected EOF,
			// network drop, desynced stream): disconnect — continuing would
			// spin forever on a dead connection.
			return err == io.EOF
		}

		// decrypt if needed
		if key != nil {
			foreignClipboardBytes, err = decrypt(key, foreignClipboardBytes)
			if err != nil {
				handleError(err)
				continue
			}
		}

		foreignClipboard = string(foreignClipboardBytes)
		// Empty means "no text on the peer" — cleared clipboard, startup sync,
		// or non-text content (image/file) that the OS reports as "". Applying
		// it would wipe this device's clipboard; clipport is text-only, so we
		// drop it. MonitorLocalClip also refuses to emit empty frames; this
		// receive-side check is the backstop for older peers.
		if foreignClipboard == "" {
			continue
		}
		setLocalClip(foreignClipboard)
		mu.Lock()
		localClipboard = foreignClipboard
		mu.Unlock()
		debug("rcvd:", foreignClipboard)
		type dropInfo struct {
			addr   string
			secure bool
		}
		var dropped []dropInfo
		mu.Lock()
		for i := range listOfClients {
			if listOfClients[i] != nil {
				err = sendClipboard(listOfClients[i].w, foreignClipboard, listOfClients[i].key)
				if err != nil {
					dropped = append(dropped, dropInfo{addr: listOfClients[i].addr, secure: listOfClients[i].key != nil})
					listOfClients[i] = nil
				}
			}
		}
		mu.Unlock()
		for _, d := range dropped {
			if d.secure {
				fmt.Printf("warning: lost connection to %s. If the peer reconnects, their identity will be re-verified.\n", d.addr)
			} else {
				fmt.Printf("warning: lost connection to %s (unencrypted). This device cannot be safely re-admitted without identity verification.\n", d.addr)
				fmt.Println("Use -k or -s to enable secure reconnections.")
			}
		}
	}
}

// sendClipboard encrypts data with key if non-nil, then sends it
func sendClipboard(w *bufio.Writer, clipboard string, key []byte) error {
	var clipboardBytes []byte
	var err error
	clipboardBytes = []byte(clipboard)
	if key != nil {
		clipboardBytes, err = encrypt(key, clipboardBytes)
		if err != nil {
			return err
		}
	}
	if len(clipboardBytes) > maxClipboardFrameBytes {
		return fmt.Errorf("%w: %d bytes (limit %d)", errClipboardTooLarge, len(clipboardBytes), maxClipboardFrameBytes)
	}

	err = gob.NewEncoder(w).Encode(clipboardBytes)
	if err != nil {
		return err
	}
	debug("sent:", clipboard)
	return w.Flush()
}

// Thanks to https://bruinsslot.jp/post/golang-crypto/ for crypto logic

func newAESGCM(key []byte) (cipher.AEAD, error) {
	blockCipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blockCipher)
}

func encrypt(key, data []byte) ([]byte, error) {
	key, salt, err := deriveKey(key, nil)
	if err != nil {
		return nil, err
	}
	gcm, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	ciphertext = append(ciphertext, salt...)
	return ciphertext, nil
}

func decrypt(key, data []byte) ([]byte, error) {
	if len(data) < 32 {
		return nil, errors.New("ciphertext too short")
	}
	salt, data := data[len(data)-32:], data[:len(data)-32]
	key, _, err := deriveKey(key, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newAESGCM(key)
	if err != nil {
		return nil, err
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

func deriveKey(password, salt []byte) ([]byte, []byte, error) {
	if salt == nil {
		salt = make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return nil, nil, err
		}
	}
	key, err := scrypt.Key(password, salt, cryptoStrength, 8, 1, 32)
	if err != nil {
		return nil, nil, err
	}
	return key, salt, nil
}

func runGetClipCommand() string {
	var out []byte
	var err error
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbpaste")
	case "windows": //nolint // complains about literal string "windows" being used multiple times
		cmd = exec.Command("powershell.exe", "-command", "Get-Clipboard")
	default:
		cmd, err = linuxClipboardCommand(true)
		if err != nil {
			handleError(err)
			os.Exit(2)
		}
	}
	if out, err = cmd.Output(); err != nil {
		// Unreadable clipboard (non-text, e.g. image): report once per
		// failure streak, then stay quiet. Return "" so MonitorLocalClip
		// does not put an error sentinel on the wire (uniclip#23).
		reportClipReadFailure(err)
		return ""
	}
	reportClipReadSuccess()
	if runtime.GOOS == "windows" {
		return normalizeWindowsClip(string(out))
	}
	return string(out)
}

// reportClipReadFailure logs a clipboard read error at most once until
// reportClipReadSuccess runs — non-text content would otherwise print
// every poll (uniclip#23).
func reportClipReadFailure(err error) {
	if clipReadErrReported.CompareAndSwap(false, true) {
		handleError(fmt.Errorf("cannot read clipboard as text (non-text content?): %w", err))
		fmt.Fprintln(os.Stderr, "error: suppressing further clipboard read errors until a read succeeds")
	}
}

func reportClipReadSuccess() {
	clipReadErrReported.Store(false)
}

// linuxClipboardCommand picks a clipboard utility for Linux/BSD/Termux.
// On a Wayland session ($WAYLAND_DISPLAY set) wl-paste/wl-copy are tried
// first so a Wayland box that also has xclip does not pick the X11 tool
// and fail with exit status 1 (uniclip#26). Otherwise the historical
// order is kept: xclip, xsel, wl-*, termux.
func linuxClipboardCommand(get bool) (*exec.Cmd, error) {
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	type tool struct {
		name string
		args []string
	}
	var tools []tool
	if wayland {
		if get {
			tools = append(tools, tool{"wl-paste", []string{"--no-newline"}})
		} else {
			tools = append(tools, tool{"wl-copy", nil})
		}
	}
	if get {
		tools = append(tools,
			tool{"xclip", []string{"-out", "-selection", "clipboard"}},
			tool{"xsel", []string{"--output", "--clipboard"}},
			tool{"wl-paste", []string{"--no-newline"}},
			tool{"termux-clipboard-get", nil},
		)
	} else {
		tools = append(tools,
			tool{"xclip", []string{"-in", "-selection", "clipboard"}},
			tool{"xsel", []string{"--input", "--clipboard"}},
			tool{"wl-copy", nil},
			tool{"termux-clipboard-set", nil},
		)
	}
	for _, t := range tools {
		if _, err := exec.LookPath(t.name); err == nil {
			// #nosec G204 -- t.name is from the fixed allowlist above, not user input
			return exec.Command(t.name, t.args...), nil
		}
	}
	if get {
		return nil, errors.New("sorry, clipport won't work if you don't have xsel, xclip, wayland or Termux installed :(\nyou can create an issue at https://github.com/tsyche/clipport/issues")
	}
	return nil, errors.New("sorry, clipport won't work if you don't have xsel, xclip, wayland or Termux:API installed :(\nyou can create an issue at https://github.com/tsyche/clipport/issues")
}

// normalizeWindowsClip converts PowerShell Get-Clipboard output to LF-only
// text. Get-Clipboard rewrites every LF as CRLF and appends a trailing CRLF;
// leaving internal CRLFs intact corrupted multi-line pastes on peers
// (uniclip#35 / uniclip#36).
func normalizeWindowsClip(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimSuffix(s, "\n")
}

func runSetClipCommand(s string) {
	var copyCmd *exec.Cmd
	var err error
	switch runtime.GOOS {
	case "darwin":
		copyCmd = exec.Command("pbcopy")
	case "windows":
		copyCmd = exec.Command("clip")
	default:
		copyCmd, err = linuxClipboardCommand(false)
		if err != nil {
			handleError(err)
			os.Exit(2)
		}
	}
	in, err := copyCmd.StdinPipe()
	if err != nil {
		handleError(err)
		return
	}
	if err = copyCmd.Start(); err != nil {
		handleError(err)
		return
	}
	if _, err = in.Write([]byte(s)); err != nil {
		handleError(err)
		return
	}
	if err = in.Close(); err != nil {
		handleError(err)
		return
	}
	if err = copyCmd.Wait(); err != nil {
		handleError(err)
		return
	}
}

func getOutboundIP() net.IP {
	// https://stackoverflow.com/questions/23558425/how-do-i-get-the-local-ip-address-in-go/37382208#37382208
	conn, err := net.Dial("udp", "8.8.8.8:80") // address can be anything. Doesn't even have to exist
	if err != nil {
		handleError(err)
		return nil
	}
	defer conn.Close()
	localAddr := conn.LocalAddr().(*net.UDPAddr)
	return localAddr.IP
}

func handleError(err error) {
	if err == io.EOF {
		info("Disconnected")
	} else {
		fmt.Fprintln(os.Stderr, "error: ["+err.Error()+"]")
	}
}

// isNetworkDisconnect reports whether err is a network-level disconnect
// (timeout, broken pipe, connection reset, etc.) on an already-established
// connection. Used to suppress noisy-but-expected errors when a peer drops.
func isNetworkDisconnect(err error) bool {
	var netErr *net.OpError
	return errors.As(err, &netErr)
}

func debug(a ...interface{}) {
	if printDebugInfo {
		fmt.Println("verbose:", a)
	}
}

// info/infof print status chatter unless --quiet is set. Errors (handleError →
// stderr), interactive prompts, security warnings, and direct subcommand output
// (keygen, known-hosts) never route through these — they always print.
func info(a ...interface{}) {
	if !quiet {
		fmt.Println(a...)
	}
}

func infof(format string, a ...interface{}) {
	if !quiet {
		fmt.Printf(format, a...)
	}
}
