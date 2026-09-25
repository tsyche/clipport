package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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
       clipport key fingerprint
       clipport key rotate
       clipport known-hosts [list|remove <peer>]
       clipport status
       clipport doctor
Examples:
   clipport                                   # start a new clipboard with randomized port
   clipport -p 6666                           # start a new clipboard on a set port number
   clipport -d                                # start a new clipboard with debug output
   clipport 192.168.86.24:53701               # join the clipboard at 192.168.86.24:53701
   clipport 192.168.86.24 -p 53701            # same as above, host and port given separately
   clipport [fe80::1]:53701                   # join via IPv6 (bracketed form; bare ::1 -p 53701 also works)
   clipport -d --secure 192.168.86.24:53701   # join the clipboard with debug output and enable encryption
      clipport keygen                            # generate a clipport keypair for use with --key
      clipport key fingerprint                   # re-print this device's key fingerprint (verify out of band)
      clipport key rotate                        # back up and regenerate this device's key (peers re-verify)
     clipport -k 192.168.86.24:53701            # join using keypair-based encryption instead of a password
    clipport known-hosts                       # list trusted -k peers
    clipport known-hosts remove 192.168.86.24  # forget a peer (after key rotation; peer IDs are host-only)
    clipport status                           # list connected clients of the running server
    clipport doctor                           # diagnose clipboard backend, keys, state, and listener
Running just ` + "`clipport`" + ` will start a new clipboard.
It will also provide an address with which you can connect to the same clipboard with another device.
With --secure, the password is read from --password-file, the CLIPPORT_SECRET (or
CLIPPORT_PASSWORD) environment variable, or an interactive prompt — the file and the
environment are mutually exclusive. Set the same password on both machines to skip
the prompt on both ends.
With --key, each device uses its own keypair (run ` + "`clipport keygen`" + ` once per device) and no
secret ever has to be typed or shared; the first connection to a given peer trusts its public key and
remembers it under ~/.clipport/known_peers, warning loudly if that peer's key ever changes later.
Connecting without --secure or --key will prompt for confirmation since the clipboard is sent in plaintext;
set CLIPPORT_ALLOW_PLAINTEXT=1 to skip that prompt for headless/scripted runs (the default stays interactive).
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

	// passwordFile is the --password-file flag: read the --secure password
	// from a file instead of CLIPPORT_SECRET/CLIPPORT_PASSWORD or a prompt.
	passwordFile = ""

	// Clipboard access is routed through vars so tests can stub the system
	// clipboard without depending on pbpaste/xclip being present or writable.
	getLocalClip = runGetClipCommand
	setLocalClip = runSetClipCommand

	// clipReadErrReported latches after the first clipboard read failure so
	// an unreadable clipboard does not spam handleError every poll
	// (uniclip#23). Cleared when a read succeeds.
	clipReadErrReported atomic.Bool

	// oversizeFrameReported latches after the first skipped oversize frame
	// so a persistent over-limit clipboard prints one warning, not one per
	// change streak. Cleared when a frame sends successfully.
	oversizeFrameReported atomic.Bool

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

	// Server-side stale-slot pruning (see watchServerWake/pruneStaleClients).
	// Package vars so tests can shorten the timings.
	staleProbeTimeout = 3 * time.Second // per-peer write deadline for one probe
	serverWakePoll    = time.Second     // clock sampling interval on the server
	serverWakeSettle  = 2 * time.Second // network reassociation wait after wake
	// pruneStale is what the wake watcher calls; tests stub it to observe runs.
	pruneStale = pruneStaleClients

	// reserveClientSlotWithPrune polls slotReleaseTries times at
	// slotReleasePoll after a prune pass, waiting for HandleClient to release
	// a freed slot (closing a dead conn only unblocks that cleanup — the slot
	// itself frees a moment later). Total budget ≈ tries × poll; tests shorten
	// the tries.
	slotReleasePoll  = 25 * time.Millisecond
	slotReleaseTries = 20

	// prunePassCooldown is the minimum interval between joiner-triggered
	// stale-probe passes on the accept loop: without it, every rejected
	// joiner at capacity serializes probe writes under the clipboard lock.
	// Wake-triggered passes always run and refresh the same window, so a
	// joiner right after a wake skip is not probed twice either. Package var
	// so tests can shorten it.
	prunePassCooldown = 5 * time.Second
	// lastPrunePass is the unix nanos of the most recent probe pass
	// (0 = never); refreshed by runPrunePass before probing.
	lastPrunePass atomic.Int64

	// isClientProcess marks the dialing side. Clients never host peers, so
	// they skip the receive-side re-broadcast in MonitorSentClips — a no-op
	// across separate processes, but it keeps an in-process loopback test
	// (both sides sharing one listOfClients) from echoing frames forever.
	isClientProcess atomic.Bool

	// runningServer exposes the live makeServer listener and wake-watcher
	// stop channel so tests can shut the accept loop down; guarded by mu,
	// nil when no server is running. Production never reads it.
	runningServer *serverHandle

	// exitProcess is os.Exit; tests stub it to observe shutdown without
	// killing the test binary.
	exitProcess = os.Exit
)

// serverHandle is the test-facing handle on a running makeServer (see
// runningServer).
type serverHandle struct {
	l        net.Listener
	wakeStop chan struct{}
	wakeDone chan struct{}
}

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
// for that specific connection (nil if unencrypted). conn is the underlying
// transport so stale-slot pruning can close a write-dead peer directly.
type client struct {
	w    *bufio.Writer
	key  []byte
	addr string
	conn net.Conn
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
	flag.StringVar(&passwordFile, "password-file", "", "Read the --secure password from a file instead of the environment or a prompt")
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
	if len(args) >= 1 && args[0] == "key" {
		if err := runKeyCommand(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
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
	if len(args) == 1 && args[0] == "doctor" {
		runDoctor(port)
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
	if passwordFile != "" && (!secure || keyMode) {
		fmt.Fprintln(os.Stderr, "error: --password-file requires -s/--secure")
		os.Exit(1)
	}

	if !secure {
		if plaintextOptIn() {
			fmt.Println("Warning: continuing in plaintext (CLIPPORT_ALLOW_PLAINTEXT=1).")
		} else if !confirmPlaintext() {
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

// resolvePassword returns the secure-mode password: --password-file if given,
// else CLIPPORT_SECRET / CLIPPORT_PASSWORD, else an interactive prompt.
func resolvePassword() []byte {
	pw, found, err := passwordFromSources(passwordFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if found {
		return pw
	}
	fmt.Print("Password for --secure: ")
	pw, err = term.ReadPassword(int(syscall.Stdin)) //nolint:unconvert // syscall.Stdin's underlying type differs across the cross-compiled GOOS targets; the cast is a no-op on linux but required elsewhere
	fmt.Println()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: could not read password:", err)
		os.Exit(1)
	}
	return pw
}

// passwordFromSources resolves the non-interactive password sources for -s:
// --password-file (trailing newlines stripped) or the CLIPPORT_SECRET /
// CLIPPORT_PASSWORD environment variables. File and environment are mutually
// exclusive (as are the two env names) so a misconfigured deployment fails
// loudly instead of silently authenticating with the wrong secret. found=false
// means no source is set and the caller should prompt.
func passwordFromSources(file string) ([]byte, bool, error) {
	secret := os.Getenv("CLIPPORT_SECRET")
	alias := os.Getenv("CLIPPORT_PASSWORD")
	if secret != "" && alias != "" {
		return nil, false, errors.New("set only one of CLIPPORT_SECRET or CLIPPORT_PASSWORD, not both")
	}
	env := secret
	if env == "" {
		env = alias
	}
	if file != "" && env != "" {
		return nil, false, errors.New("use either --password-file or CLIPPORT_SECRET/CLIPPORT_PASSWORD, not both")
	}
	if file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, false, fmt.Errorf("could not read password file: %w", err)
		}
		pw := strings.TrimRight(string(raw), "\r\n")
		if pw == "" {
			return nil, false, fmt.Errorf("password file %s is empty", file)
		}
		return []byte(pw), true, nil
	}
	if env != "" {
		return []byte(env), true, nil
	}
	return nil, false, nil
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

// plaintextOptIn reports whether CLIPPORT_ALLOW_PLAINTEXT=1 explicitly skips
// the interactive plaintext confirmation, so launchd/systemd and other
// headless deployments can start unattended. Anything but the exact value
// "1" keeps the prompt gate.
func plaintextOptIn() bool {
	return os.Getenv("CLIPPORT_ALLOW_PLAINTEXT") == "1"
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

// runKeyCommand implements `clipport key <subcommand>`: `fingerprint`
// re-prints this device's own key fingerprint so users can verify it out of
// band without regenerating the key (known-hosts list shows trusted peers
// only, closing half of the TOFU loop); `rotate` backs up the current
// keypair and generates a fresh one with a peer re-verification reminder.
func runKeyCommand(args []string) error {
	if len(args) != 1 {
		return keyUsage()
	}
	switch args[0] {
	case "fingerprint":
		fp, err := ownFingerprint()
		if err != nil {
			return err
		}
		fmt.Println("Fingerprint:", fp)
		return nil
	case "rotate":
		return rotateKey()
	default:
		return keyUsage()
	}
}

func keyUsage() error {
	return errors.New("usage: clipport key fingerprint | clipport key rotate")
}

// rotateKey replaces the device's keypair: the old private/public key files
// are renamed to timestamped .bak paths (a recoverable backup, not a
// destructive overwrite), a fresh keypair is generated, and both
// fingerprints print. Peers that trusted the old key will fail
// verification on the next connection until they
// `clipport known-hosts remove <this-host>` after checking the new
// fingerprint out of band — hence the reminder.
func rotateKey() error {
	dir, err := clipportDir()
	if err != nil {
		return err
	}
	keyPath := filepath.Join(dir, "key")
	pubPath := filepath.Join(dir, "key.pub")
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return errors.New("no clipport key found, run `clipport keygen` first")
		}
		return err
	}
	oldFP := ""
	if oldPriv, err := ecdh.X25519().NewPrivateKey(raw); err == nil {
		oldFP = fingerprint(oldPriv.PublicKey().Bytes())
	}
	stamp := time.Now().Format("20060102-150405")
	backup := fmt.Sprintf("%s.%s.bak", keyPath, stamp)
	if err := os.Rename(keyPath, backup); err != nil {
		return fmt.Errorf("could not back up existing key: %w", err)
	}
	pubBackup := ""
	if _, err := os.Stat(pubPath); err == nil {
		pubBackup = fmt.Sprintf("%s.%s.bak", pubPath, stamp)
		if err := os.Rename(pubPath, pubBackup); err != nil {
			// Put the private key back so the device is left as it was.
			if rbErr := os.Rename(backup, keyPath); rbErr != nil {
				fmt.Fprintln(os.Stderr, "warning: could not restore previous key:", rbErr)
			}
			return fmt.Errorf("could not back up existing public key: %w", err)
		}
	}
	newPath, pub, err := generateKeypair(dir)
	if err != nil {
		// Restore the previous keypair; nothing has changed on disk yet.
		if rbErr := os.Rename(backup, keyPath); rbErr != nil {
			fmt.Fprintln(os.Stderr, "warning: could not restore previous key:", rbErr)
		}
		if pubBackup != "" {
			if rbErr := os.Rename(pubBackup, pubPath); rbErr != nil {
				fmt.Fprintln(os.Stderr, "warning: could not restore previous public key:", rbErr)
			}
		}
		return fmt.Errorf("could not generate replacement key: %w", err)
	}
	fmt.Println("Backed up previous key to", backup)
	fmt.Println("Generated new clipport key at", newPath)
	if oldFP != "" {
		fmt.Println("Previous fingerprint:", oldFP)
	}
	fmt.Println("Fingerprint:", fingerprint(pub))
	fmt.Println("Peers that trusted the previous key will reject it until they run `clipport known-hosts remove <this-host>` after verifying the new fingerprint out of band.")
	return nil
}

// ownFingerprint returns the fingerprint of this device's own keypair —
// the same value `clipport keygen` printed at generation time.
func ownFingerprint() (string, error) {
	priv, err := loadKeypair()
	if err != nil {
		return "", err
	}
	return fingerprint(priv.PublicKey().Bytes()), nil
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

// doctorCheck is one diagnostic result `clipport doctor` reports. status is
// doctorOK or doctorFail; only doctorFail makes the command exit non-zero.
type doctorCheck struct {
	status string
	name   string
	detail string
}

// doctorOK/doctorFail are the doctorCheck status values.
const (
	doctorOK   = "ok"
	doctorFail = "fail"
)

// runDoctor implements `clipport doctor`: a read-only diagnostic battery
// (clipboard backend, state dir, keypair, known-hosts, running server,
// listener reachability) that points first-run failures at the fix. Direct
// subcommand output, not gated by --quiet; exits 1 if any check fails.
func runDoctor(port string) {
	checks := collectDoctorChecks(port)
	fails := 0
	for _, c := range checks {
		if c.status == doctorFail {
			fails++
		}
		fmt.Printf("  %-4s %s: %s\n", c.status, c.name, c.detail)
	}
	fmt.Printf("%d checks, %d failure(s)\n", len(checks), fails)
	if fails > 0 {
		os.Exit(1)
	}
}

// collectDoctorChecks runs the diagnostic battery. port is the -p flag value
// ("" when unpinned; only used for the bind test when no server runs).
func collectDoctorChecks(port string) []doctorCheck {
	checks := make([]doctorCheck, 0, 6)
	if detail, err := clipboardBackendDetail(); err != nil {
		checks = append(checks, doctorCheck{doctorFail, "clipboard backend", err.Error()})
	} else {
		checks = append(checks, doctorCheck{doctorOK, "clipboard backend", detail})
	}
	dir, err := clipportDir()
	if err != nil {
		return append(checks, doctorCheck{doctorFail, "state dir", err.Error()})
	}
	perm := "missing"
	if fi, statErr := os.Stat(dir); statErr == nil {
		perm = fmt.Sprintf("mode %04o", fi.Mode().Perm())
	}
	checks = append(checks, doctorCheck{doctorOK, "state dir", fmt.Sprintf("%s (%s)", dir, perm)})
	checks = append(checks, doctorKeypairCheck(dir))
	peers, err := loadKnownPeers(filepath.Join(dir, "known_peers"))
	if err != nil {
		checks = append(checks, doctorCheck{doctorFail, "known-hosts", err.Error()})
	} else {
		checks = append(checks, doctorCheck{doctorOK, "known-hosts", fmt.Sprintf("%d trusted peer(s)", len(peers))})
	}
	serverPort := ""
	if st, stErr := queryStatus(dir); stErr == nil {
		serverPort = st.Port
		checks = append(checks, doctorCheck{doctorOK, "server",
			fmt.Sprintf("running (pid %d, port %s, %d client(s))", st.Pid, st.Port, len(st.Clients))})
	} else {
		checks = append(checks, doctorCheck{doctorOK, "server", "not running (start one with `clipport`)"})
	}
	return append(checks, doctorListenerCheck(port, serverPort))
}

// clipboardBackendDetail reports which clipboard tools the platform will use,
// failing when none of the supported backends are on PATH — the same absence
// that would make runGetClipCommand/runSetClipCommand exit at runtime. Probe
// only: never reads or writes the clipboard.
func clipboardBackendDetail() (string, error) {
	switch runtime.GOOS {
	case osDarwin:
		if err := requireOnPATH("pbpaste", "pbcopy"); err != nil {
			return "", fmt.Errorf("clipboard tool(s) missing from PATH: %w", err)
		}
		return "pbpaste/pbcopy (macOS)", nil
	case "windows": //nolint // literal "windows" is used in multiple switches
		if err := requireOnPATH("powershell.exe", "clip"); err != nil {
			return "", fmt.Errorf("clipboard tool(s) missing from PATH: %w", err)
		}
		return "powershell/clip (Windows)", nil
	default:
		return linuxBackendDetail()
	}
}

// requireOnPATH returns an error naming every tool not found on PATH.
func requireOnPATH(names ...string) error {
	var missing []string
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return errors.New(strings.Join(missing, ", "))
	}
	return nil
}

// linuxBackendDetail resolves the get/set tools through the same
// linuxClipboardCommand picker the monitors use, so doctor reports exactly
// what a session (Wayland or X11) would run.
func linuxBackendDetail() (string, error) {
	getCmd, err := linuxClipboardCommand(true)
	if err != nil {
		return "", errors.New("no clipboard tool found — install xclip, xsel, wl-clipboard, or Termux:API")
	}
	setCmd, err := linuxClipboardCommand(false)
	if err != nil {
		return "", errors.New("no clipboard writer found — install xclip, xsel, wl-clipboard, or Termux:API")
	}
	detail := cmdToolName(getCmd) + "/" + cmdToolName(setCmd)
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		detail += " (Wayland session)"
	}
	return detail, nil
}

func cmdToolName(c *exec.Cmd) string {
	if c != nil && len(c.Args) > 0 {
		return c.Args[0]
	}
	return "?"
}

// doctorKeypairCheck reports keypair presence: absent is OK (the key is only
// needed for -k mode), but an unreadable/corrupt key file is a failure.
func doctorKeypairCheck(dir string) doctorCheck {
	if _, err := os.Stat(filepath.Join(dir, "key")); err != nil {
		if os.IsNotExist(err) {
			return doctorCheck{doctorOK, "keypair", "absent (needed only for -k mode; run `clipport keygen`)"}
		}
		return doctorCheck{doctorFail, "keypair", err.Error()}
	}
	fp, err := ownFingerprint()
	if err != nil {
		return doctorCheck{doctorFail, "keypair", err.Error()}
	}
	return doctorCheck{doctorOK, "keypair", "present (fingerprint " + fp + ")"}
}

// doctorListenerCheck verifies the port clipport would listen on: with a
// running server, dial it on loopback; otherwise try to bind (the -p port if
// given, else an ephemeral one). A pinned port already taken by another
// process is a common first-run failure mode and shows up as a bind error.
func doctorListenerCheck(port, serverPort string) doctorCheck {
	if serverPort != "" {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", serverPort), 2*time.Second)
		if err != nil {
			return doctorCheck{doctorFail, "listener",
				"server port " + serverPort + " not reachable on loopback: " + err.Error()}
		}
		_ = conn.Close()
		return doctorCheck{doctorOK, "listener", "server port " + serverPort + " reachable on loopback"}
	}
	addr, label := ":0", "ephemeral port"
	if port != "" {
		addr, label = ":"+port, "port "+port
	}
	l, err := net.Listen("tcp", addr) //nolint // all-interfaces bind mirrors makeServer; this is a doctor bind test, not a public service
	if err != nil {
		return doctorCheck{doctorFail, "listener", "cannot bind " + label + ": " + err.Error()}
	}
	bound := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	return doctorCheck{doctorOK, "listener", "can bind " + label + " (" + bound + ")"}
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

// reserveClientSlotWithPrune reserves a --max-clients slot. When the server
// is full it first runs one stale-probe pass (pruneStale) so write-dead peers
// — e.g. one that never came back after a previous wake — free their slots
// instead of a healthy joiner being rejected. Slot release is async: closing
// a dead conn unblocks HandleClient, whose cleanup calls releaseClientSlot a
// moment later, so after pruning it polls briefly for a slot to open. Probe
// passes are rate-limited by prunePassCooldown: a flood of rejected joiners
// cannot turn the accept loop into continuous probing under the clipboard
// lock (a joiner inside the cooldown window skips straight to the poll,
// which also catches slot releases from the previous pass still in flight).
func reserveClientSlotWithPrune() bool {
	if tryReserveClientSlot() {
		return true
	}
	if prunePassDue() {
		runPrunePass()
	}
	for i := 0; i < slotReleaseTries; i++ {
		time.Sleep(slotReleasePoll)
		if tryReserveClientSlot() {
			return true
		}
	}
	return false
}

// prunePassDue reports whether a joiner-triggered probe pass may run now:
// none has run yet, or at least prunePassCooldown has elapsed since the
// last pass (from either the accept loop or the wake watcher).
func prunePassDue() bool {
	last := lastPrunePass.Load()
	return last == 0 || clockNow().UnixNano()-last >= int64(prunePassCooldown)
}

// runPrunePass records the pass timestamp before probing — so a concurrent
// joiner sees the fresh window immediately — then runs one stale-probe pass.
func runPrunePass() {
	lastPrunePass.Store(clockNow().UnixNano())
	pruneStale()
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
	h := &serverHandle{l: l, wakeStop: make(chan struct{}), wakeDone: make(chan struct{})}
	mu.Lock()
	runningServer = h
	mu.Unlock()
	isClientProcess.Store(false)
	defer func() {
		mu.Lock()
		runningServer = nil
		mu.Unlock()
	}()
	go func() {
		watchServerWake(h.wakeStop)
		close(h.wakeDone)
	}()
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
		if !reserveClientSlotWithPrune() {
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
	cl := &client{w: w, key: key, addr: c.RemoteAddr().String(), conn: c}
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

// watchServerWake samples the wall clock on the server; a gap ≥
// wakeGapThreshold means the server machine suspended (same technique
// client-side wake detection uses). After a short settle for the network to
// reassociate, it probes peers and closes write-dead ones so their
// --max-clients slots free promptly instead of waiting out TCP keepalive
// (minutes) while a returning peer is rejected as "server full". stop is nil
// in production (runs for process lifetime); tests close it to join the
// goroutine before restoring stubbed globals.
func watchServerWake(stop <-chan struct{}) {
	last := clockNow()
	for {
		select {
		case <-stop:
			return
		case <-time.After(serverWakePoll):
		}
		now := clockNow()
		if now.Sub(last) >= wakeGapThreshold {
			debug("server resume detected (poll gap", now.Sub(last), "); probing for stale clients")
			time.Sleep(serverWakeSettle)
			runPrunePass()
			last = clockNow()
		} else {
			last = now
		}
	}
}

// pruneStaleClients probes every listed peer and closes the write-dead ones.
// Closing a dead conn unblocks HandleClient, whose existing cleanup removes
// the list entry and releases the slot. Live peers are never closed: a clean
// EOF there would make healthy clients exit (they treat it as server
// shutdown — the failure mode the shipped sleep/wake work deliberately
// avoided on the server side).
func pruneStaleClients() {
	mu.Lock()
	targets := make([]*client, len(listOfClients))
	copy(targets, listOfClients)
	mu.Unlock()
	for _, cl := range targets {
		if cl == nil || cl.conn == nil {
			continue
		}
		if !probeClient(cl) {
			debug("pruning write-dead client", cl.addr)
			_ = cl.conn.Close()
		}
	}
}

// probeClient sends one empty clipboard frame to cl under staleProbeTimeout —
// a frame receivers already discard (MonitorSentClips drops empty payloads),
// so the probe is invisible to healthy peers. Returns false when the write
// fails, meaning the connection is dead (typical right after server resume,
// before TCP keepalive would notice). Returns true — skipping the probe — if
// cl is no longer listed or the deadline cannot be armed (probing without a
// deadline could block the shared write path). Runs under mu so the probe
// cannot interleave with other writes to the same bufio.Writer.
func probeClient(cl *client) bool {
	mu.Lock()
	defer mu.Unlock()
	if !clientListed(cl) {
		return true
	}
	if err := cl.conn.SetWriteDeadline(clockNow().Add(staleProbeTimeout)); err != nil {
		debug("skipping stale probe for", cl.addr, ":", err)
		return true
	}
	err := sendClipboard(cl.w, "", cl.key)
	_ = cl.conn.SetWriteDeadline(time.Time{}) //nolint:errcheck // best-effort: clearing the deadline; a failed clear only leaves the (already finished) probe deadline in place
	if err != nil {
		debug("stale probe failed for", cl.addr, ":", err)
		return false
	}
	return true
}

// clientListed reports whether target is still in listOfClients. Callers must
// hold mu.
func clientListed(target *client) bool {
	for _, cl := range listOfClients {
		if cl == target {
			return true
		}
	}
	return false
}

// Connect to the server (which starts a new clipboard), reconnecting
// automatically if the connection drops while the server is still up.
func ConnectToServer(address string) {
	const (
		baseBackoff = 3 * time.Second
		maxBackoff  = 30 * time.Second
	)
	isClientProcess.Store(true)
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
// cleared clipboard, at startup, and whenever the OS clipboard holds content
// clipport cannot read (formats beyond PNG/JPEG). Sending those would wipe
// peers; see MonitorSentClips for the receive-side backstop. PNG/JPEG
// payloads are sent as-is and sniffed by the receiver.
// The initial snapshot sends immediately; subsequent changes pass through
// a quiet-window debounce so a burst of edits puts one frame (the final
// value) on the wire instead of one per poll. Oversize payloads never fail
// the connection: images are re-encoded under the frame cap when possible,
// otherwise the frame is skipped with a single warning (see sendFrame).
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
		// Send under mu: writes to any client-facing bufio.Writer must be
		// serialized with pruneStaleClients' probes (and with broadcast writes
		// in MonitorSentClips), or a probe write deadline could abort a live
		// clipboard send mid-frame.
		var sendErr error
		if cur != "" {
			if sendErr = sendFrame(w, cur, key); sendErr == nil {
				lastClipPush.Store(time.Now().UnixNano())
			}
		}
		mu.Unlock()
		debug("localClipboard changed. localClipboard =", cur)
		// localClipboard is already updated above even when cur is empty, so
		// the change-poll below does not spin while the clipboard stays empty.
		if sendErr != nil {
			if !isNetworkDisconnect(sendErr) {
				handleError(sendErr)
			}
			return
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
			if clipboardStateChanged(last, getLocalClip()) {
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
		// Empty means "no content on the peer" — cleared clipboard or a
		// format clipport could not read. Applying it would wipe this
		// device's clipboard, so we drop it. MonitorLocalClip also refuses
		// to emit empty frames; this receive-side check is the backstop for
		// older peers. Non-empty payloads (text or PNG/JPEG, sniffed by
		// runSetClipCommand) are applied as-is.
		if foreignClipboard == "" {
			continue
		}
		setLocalClip(foreignClipboard)
		mu.Lock()
		localClipboard = foreignClipboard
		mu.Unlock()
		debug("rcvd:", foreignClipboard)
		if isClientProcess.Load() {
			// A client hosts no peers: applying the received value is the
			// whole job. Skipping the re-broadcast is a no-op across real
			// processes (a client's listOfClients is empty) and stops an
			// in-process loopback test — both sides sharing one list —
			// from echoing frames between the two monitors forever.
			continue
		}
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

// sendFrame puts payload on the wire. An oversize image is re-encoded smaller
// and retried; anything still over the cap is skipped with one warning per
// streak — never a connection failure, which would make peers reconnect (and
// potentially resend) in a loop. Returns an error only for real send failures.
func sendFrame(w *bufio.Writer, payload string, key []byte) error {
	err := sendClipboard(w, payload, key)
	if err == nil {
		oversizeFrameReported.Store(false)
		return nil
	}
	if !errors.Is(err, errClipboardTooLarge) {
		return err
	}
	if isImagePayload(payload) {
		if shrunk, serr := shrinkImageToFit(payload); serr == nil {
			if err2 := sendClipboard(w, shrunk, key); err2 == nil {
				oversizeFrameReported.Store(false)
				debug("shrunk oversize image frame to", len(shrunk), "bytes")
				return nil
			} else if !errors.Is(err2, errClipboardTooLarge) {
				return err2
			}
		}
	}
	if oversizeFrameReported.CompareAndSwap(false, true) {
		fmt.Fprintf(os.Stderr,
			"warning: clipboard frame is %d bytes (limit %d); not sent until clipboard changes\n",
			len(payload), maxClipboardFrameBytes)
	}
	return nil
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
	case osDarwin:
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
	out, err = cmd.Output()
	if err == nil && len(out) > 0 {
		reportClipReadSuccess()
		if runtime.GOOS == "windows" {
			return normalizeWindowsClip(string(out))
		}
		return string(out)
	}
	// Text empty or unreadable: the clipboard may hold an image (PNG/JPEG),
	// which text tools report as "" or a non-zero exit (uniclip#23).
	if img, imgErr := readLocalImage(); imgErr == nil && len(img) > 0 {
		reportClipReadSuccess()
		return string(img)
	}
	if err != nil {
		// Neither text nor image readable: report once per failure streak
		// and return "" so MonitorLocalClip never puts an error sentinel
		// on the wire (uniclip#23).
		reportClipReadFailure(err)
		return ""
	}
	reportClipReadSuccess()
	return ""
}

// reportClipReadFailure logs a clipboard read error at most once until
// reportClipReadSuccess runs — an unreadable clipboard would otherwise print
// every poll (uniclip#23).
func reportClipReadFailure(err error) {
	if clipReadErrReported.CompareAndSwap(false, true) {
		handleError(fmt.Errorf("cannot read clipboard: %w", err))
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

var (
	pngMagic  = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	jpegMagic = []byte{0xFF, 0xD8, 0xFF}
)

// Image payload format tags (shared literals for goconst).
const (
	formatPNG  = "png"
	formatJPEG = "jpeg"
)

// osDarwin is runtime.GOOS for macOS (constant for goconst).
const osDarwin = "darwin"

// isImagePayload reports whether s carries a PNG or JPEG blob rather than
// text. Receiver-side sniffing keeps the wire format unchanged: frames are
// still gob-encoded []byte, and only these magic prefixes route to the image
// clipboard path.
func isImagePayload(s string) bool {
	return imagePayloadFormat(s) != ""
}

func imagePayloadFormat(s string) string {
	b := []byte(s)
	if len(b) >= len(pngMagic) && bytes.Equal(b[:len(pngMagic)], pngMagic) {
		return "png"
	}
	if len(b) >= len(jpegMagic) && bytes.Equal(b[:len(jpegMagic)], jpegMagic) {
		return formatJPEG
	}
	return ""
}

// imageFingerprint hashes decoded pixels (re-encoded deterministically as
// PNG) so a clipboard roundtrip that re-encodes the same picture still
// compares equal — otherwise peers would echo images back and forth forever.
// Undecodable payloads fall back to a raw-byte hash.
func imageFingerprint(s string) [32]byte {
	img, _, err := image.Decode(strings.NewReader(s))
	if err != nil {
		return sha256.Sum256([]byte(s))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return sha256.Sum256([]byte(s))
	}
	return sha256.Sum256(buf.Bytes())
}

// shrinkImageToFit re-encodes an image payload as JPEG on a downscale ×
// quality ladder until it fits under the wire frame cap (minus headroom for
// gob/AES-GCM overhead). Returns errClipboardTooLarge when no step fits —
// callers then skip the frame instead of dropping the connection.
func shrinkImageToFit(payload string) (string, error) {
	img, _, err := image.Decode(strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	limit := maxClipboardFrameBytes - (64 << 10)
	scales := []int{1, 2, 3, 4, 6, 8}
	qualities := []int{85, 70, 55, 40, 25}
	for _, div := range scales {
		scaled := img
		if div > 1 {
			scaled = downscaleBox(img, div)
		}
		for _, q := range qualities {
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, scaled, &jpeg.Options{Quality: q}); err != nil {
				return "", err
			}
			if buf.Len() <= limit {
				return buf.String(), nil
			}
		}
	}
	return "", errClipboardTooLarge
}

// downscaleBox averages div×div source pixels into one destination pixel —
// a dependency-free box filter (stdlib has no scaler; x/image is overkill).
func downscaleBox(src image.Image, div int) image.Image {
	b := src.Bounds()
	w, h := b.Dx()/div, b.Dy()/div
	if w < 1 || h < 1 {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint64(div * div)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, a uint64
			for dy := 0; dy < div; dy++ {
				for dx := 0; dx < div; dx++ {
					pr, pg, pb, pa := src.At(b.Min.X+x*div+dx, b.Min.Y+y*div+dy).RGBA()
					r += uint64(pr)
					g += uint64(pg)
					bl += uint64(pb)
					a += uint64(pa)
				}
			}
			dst.Set(x, y, color.RGBA64{
				R: uint16(r / n),  // #nosec G115 -- color.Color.RGBA() returns 16-bit channels
				G: uint16(g / n),  // #nosec G115 -- color.Color.RGBA() returns 16-bit channels
				B: uint16(bl / n), // #nosec G115 -- color.Color.RGBA() returns 16-bit channels
				A: uint16(a / n),  // #nosec G115 -- color.Color.RGBA() returns 16-bit channels
			})
		}
	}
	return dst
}

// clipboardStateChanged decides whether the local clipboard moved on. Text
// compares by equality; image-to-image compares by pixel fingerprint so a
// lossless re-encode of the same picture (Windows SetImage/GetImage, macOS
// class conversions) is not treated as a new copy.
func clipboardStateChanged(last, cur string) bool {
	if last == cur {
		return false
	}
	if isImagePayload(last) && isImagePayload(cur) {
		return imageFingerprint(last) != imageFingerprint(cur)
	}
	return true
}

// writeImageClip is the platform image setter; a var so tests can stub the
// image path without shelling out.
var writeImageClip = writeLocalImage

func writeLocalImage(b []byte) error {
	switch runtime.GOOS {
	case osDarwin:
		return setDarwinImage(b)
	case "windows": //nolint // literal "windows" used elsewhere too
		return setWindowsImage(b)
	default:
		return setLinuxImage(b)
	}
}

// setDarwinImage writes the payload to a private temp file and points the
// macOS clipboard at it — osascript data literals in argv hit ARG_MAX on
// large screenshots, file-based `read` does not.
func setDarwinImage(b []byte) error {
	class, ext := "PNGf", "png"
	if imagePayloadFormat(string(b)) == formatJPEG {
		class, ext = "JPEGf", "jpg"
	}
	f, err := os.CreateTemp("", "clipport-*."+ext)
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	script := fmt.Sprintf("set the clipboard to (read (POSIX file %s) as «class %s»)", strconv.Quote(name), class)
	return exec.Command("osascript", "-e", script).Run()
}

// setWindowsImage receives the blob as base64 on stdin (argv would overflow
// on large images) and installs it via System.Windows.Forms.Clipboard.
func setWindowsImage(b []byte) error {
	const script = `Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$b64 = [Console]::In.ReadToEnd()
$bytes = [Convert]::FromBase64String($b64)
$ms = New-Object System.IO.MemoryStream(,$bytes)
$img = [System.Drawing.Image]::FromStream($ms)
[System.Windows.Forms.Clipboard]::SetImage($img)`
	cmd := exec.Command("powershell.exe", "-command", script)
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString(b))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("setting image clipboard: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// setLinuxImage pipes the blob to xclip/X11 or wl-copy/Wayland with an
// explicit image MIME type.
func setLinuxImage(b []byte) error {
	mime := "image/png"
	if imagePayloadFormat(string(b)) == formatJPEG {
		mime = "image/jpeg"
	}
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	type tool struct {
		name string
		args []string
	}
	var tools []tool
	if wayland {
		tools = append(tools, tool{"wl-copy", []string{"--type", mime}})
		tools = append(tools, tool{"xclip", []string{"-in", "-selection", "clipboard", "-t", mime}})
	} else {
		tools = append(tools, tool{"xclip", []string{"-in", "-selection", "clipboard", "-t", mime}})
		tools = append(tools, tool{"wl-copy", []string{"--type", mime}})
	}
	lastErr := errors.New("no image clipboard tool found (install xclip or wl-clipboard)")
	for _, t := range tools {
		if _, err := exec.LookPath(t.name); err != nil {
			continue
		}
		// #nosec G204 -- t.name is from the fixed allowlist above, not user input
		cmd := exec.Command(t.name, t.args...)
		cmd.Stdin = bytes.NewReader(b)
		err := cmd.Run()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

// readLocalImage returns PNG/JPEG clipboard bytes, or an error when the
// clipboard holds no image the platform tools can read.
func readLocalImage() ([]byte, error) {
	switch runtime.GOOS {
	case osDarwin:
		if b, err := readOsascriptImage("PNGf"); err == nil {
			return b, nil
		}
		return readOsascriptImage("JPEGf")
	case "windows": //nolint // literal "windows" used elsewhere too
		return readWindowsImage()
	default:
		return readLinuxImage()
	}
}

func readOsascriptImage(class string) ([]byte, error) {
	// #nosec G204 -- class is a fixed internal token ("PNGf"/"JPEGf"), not user input
	out, err := exec.Command("osascript", "-e", "the clipboard as «class "+class+"»").Output()
	if err != nil {
		return nil, err
	}
	return parseOsascriptData(string(out), class)
}

// parseOsascriptData decodes the «data CLASS HEX» literal osascript prints
// for raw clipboard data.
func parseOsascriptData(out, class string) ([]byte, error) {
	out = strings.TrimSpace(out)
	prefix := "«data " + class
	if !strings.HasPrefix(out, prefix) || !strings.HasSuffix(out, "»") {
		return nil, fmt.Errorf("unexpected osascript clipboard output")
	}
	hexPart := strings.TrimSuffix(strings.TrimPrefix(out, prefix), "»")
	return hex.DecodeString(hexPart)
}

// readWindowsImage saves the system image clipboard as PNG and base64s it
// to stdout (avoids PowerShell stdout encoding mangling raw bytes).
func readWindowsImage() ([]byte, error) {
	const script = `Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$c = Get-Clipboard -Format Image -ErrorAction SilentlyContinue
if ($null -eq $c) { exit 1 }
$ms = New-Object System.IO.MemoryStream
$c.Save($ms, [System.Drawing.Imaging.ImageFormat]::Png)
[Convert]::ToBase64String($ms.ToArray())`
	out, err := exec.Command("powershell.exe", "-command", script).Output()
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
}

// readLinuxImage tries image-capable clipboard tools (xclip on X11,
// wl-paste on Wayland) for PNG, then JPEG. xsel has no image support.
func readLinuxImage() ([]byte, error) {
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	mimes := []string{"image/png", "image/jpeg"}
	type candidate struct {
		build func(mime string) *exec.Cmd
		name  string
	}
	var cands []candidate
	if wayland {
		cands = append(cands, candidate{
			name: "wl-paste",
			build: func(mime string) *exec.Cmd {
				return exec.Command("wl-paste", "--no-newline", "--type", mime)
			},
		})
		cands = append(cands, candidate{
			name: "xclip",
			build: func(mime string) *exec.Cmd {
				return exec.Command("xclip", "-out", "-selection", "clipboard", "-t", mime)
			},
		})
	} else {
		cands = append(cands, candidate{
			name: "xclip",
			build: func(mime string) *exec.Cmd {
				return exec.Command("xclip", "-out", "-selection", "clipboard", "-t", mime)
			},
		})
		cands = append(cands, candidate{
			name: "wl-paste",
			build: func(mime string) *exec.Cmd {
				return exec.Command("wl-paste", "--no-newline", "--type", mime)
			},
		})
	}
	for _, c := range cands {
		if _, err := exec.LookPath(c.name); err != nil {
			continue
		}
		for _, mime := range mimes {
			out, err := c.build(mime).Output()
			if err == nil && isImagePayload(string(out)) {
				return out, nil
			}
		}
	}
	return nil, errors.New("no image clipboard content readable (need xclip or wl-clipboard)")
}

func runSetClipCommand(s string) {
	if isImagePayload(s) {
		if err := writeImageClip([]byte(s)); err != nil {
			handleError(err)
		}
		return
	}
	var copyCmd *exec.Cmd
	var err error
	switch runtime.GOOS {
	case osDarwin:
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
