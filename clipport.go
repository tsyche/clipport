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
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"

	"golang.org/x/crypto/scrypt"
)

var (
	secondsBetweenChecksForClipChange = 1
	helpMsg                           = `Clipport - Universal Clipboard
With Clipport, you can copy from one device and paste on another.

Usage: clipport [--port/-p] [--secure/-s] [--key/-k] [--debug/-d] [ <address> | --help/-h ]
       clipport keygen
Examples:
   clipport                                   # start a new clipboard with randomized port
   clipport -p 6666                           # start a new clipboard on a set port number
   clipport -d                                # start a new clipboard with debug output
   clipport 192.168.86.24:53701               # join the clipboard at 192.168.86.24:53701
   clipport 192.168.86.24 -p 53701            # same as above, host and port given separately
   clipport -d --secure 192.168.86.24:53701   # join the clipboard with debug output and enable encryption
   clipport keygen                            # generate a clipport keypair for use with --key
   clipport -k 192.168.86.24:53701            # join using keypair-based encryption instead of a password
Running just ` + "`clipport`" + ` will start a new clipboard.
It will also provide an address with which you can connect to the same clipboard with another device.
With --secure, the password is read from the CLIPPORT_SECRET environment variable if set,
otherwise you'll be prompted for it. Set CLIPPORT_SECRET on both machines to skip the prompt on both ends.
With --key, each device uses its own keypair (run ` + "`clipport keygen`" + ` once per device) and no
secret ever has to be typed or shared; the first connection to a given peer trusts its public key and
remembers it under ~/.clipport/known_peers, warning loudly if that peer's key ever changes later.
Connecting without --secure or --key will prompt for confirmation since the clipboard is sent in plaintext.
Refer to https://github.com/tsyche/clipport for more information`
	mu             sync.Mutex
	listOfClients  = make([]*client, 0)
	localClipboard string
	printDebugInfo = false
	version        = "dev"
	cryptoStrength = 16384
	secure         = false
	keyMode        = false
	password       []byte
)

// client pairs a connected peer's writer with the encryption key negotiated
// for that specific connection (nil if unencrypted).
type client struct {
	w    *bufio.Writer
	key  []byte
	addr string
}

func main() {
	var (
		port        string
		showVersion bool
	)

	flag.StringVar(&port, "p", "", "Specify the port to listen on")
	flag.StringVar(&port, "port", "", "Specify the port to listen on")
	flag.BoolVar(&secure, "s", false, "Encrypt your data using a shared password")
	flag.BoolVar(&secure, "secure", false, "Encrypt your data using a shared password")
	flag.BoolVar(&keyMode, "k", false, "Encrypt your data using a clipport keypair (see `clipport keygen`)")
	flag.BoolVar(&keyMode, "key", false, "Encrypt your data using a clipport keypair (see `clipport keygen`)")
	flag.BoolVar(&printDebugInfo, "d", false, "Enable debug output")
	flag.BoolVar(&printDebugInfo, "debug", false, "Enable debug output")
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
	pw, _ := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	return pw
}

// resolveClientAddress combines a host (optionally with an embedded port) and
// an optional -p port into a single dialable address, erroring if both are
// given but disagree.
func resolveClientAddress(addr, port string) (string, error) {
	host, embeddedPort, err := net.SplitHostPort(addr)
	if err != nil {
		if port == "" {
			return "", errors.New("no port specified: use host:port or -p")
		}
		return addr + ":" + port, nil
	}
	if port != "" && port != embeddedPort {
		return "", fmt.Errorf("conflicting ports: %s in address vs -p %s", embeddedPort, port)
	}
	return host + ":" + embeddedPort, nil
}

// confirmPlaintext warns the user that no encryption was requested and asks
// for confirmation before continuing.
func confirmPlaintext() bool {
	fmt.Print("Warning: no encryption requested (-s or -k). Clipboard contents will be sent in plaintext. Continue? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
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
		return nil, localErr
	}
	if peerStatus[0] == 0 {
		return nil, errors.New("peer rejected the connection (its key verification failed on its end)")
	}

	return priv.ECDH(peerPub)
}

// clipportDir returns ~/.clipport, creating it if necessary.
func clipportDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".clipport")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}

// runKeygen generates a new X25519 keypair for --key mode and stores it
// under ~/.clipport, refusing to overwrite an existing key.
func runKeygen() {
	dir, err := clipportDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	keyPath := filepath.Join(dir, "key")
	if _, err := os.Stat(keyPath); err == nil {
		fmt.Fprintf(os.Stderr, "error: a key already exists at %s, remove it first to regenerate\n", keyPath)
		os.Exit(1)
	}
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	pub := priv.PublicKey().Bytes()
	if err := os.WriteFile(keyPath, priv.Bytes(), 0600); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(filepath.Join(dir, "key.pub"), pub, 0600); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println("Generated clipport key at", keyPath)
	fmt.Println("Fingerprint:", fingerprint(pub))
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
				"If this is expected, remove the %q line from %s and reconnect", peerID, peerID, path)
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

func fingerprint(pubKey []byte) string {
	sum := sha256.Sum256(pubKey)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func makeServer(port string) {
	fmt.Println("Starting a new clipboard")
	listenAddr := ":"
	if port != "" {
		listenAddr = ":" + port
	}
	l, err := net.Listen("tcp4", listenAddr) //nolint // complains about binding to all interfaces
	if err != nil {
		handleError(err)
		return
	}
	defer l.Close()
	if port == "" {
		port = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	}
	fmt.Println("Run", "`clipport", getOutboundIP().String()+":"+port+"`", "to join this clipboard")
	fmt.Println()
	for {
		c, err := l.Accept()
		if err != nil {
			handleError(err)
			return
		}
		enableKeepAlive(c)
		fmt.Println("Connected to device at " + c.RemoteAddr().String())
		go HandleClient(c)
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
	_ = tc.SetKeepAlive(true)
	_ = tc.SetKeepAlivePeriod(30 * time.Second)
}

// Handle a client as a server
func HandleClient(c net.Conn) {
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
	go MonitorSentClips(bufio.NewReader(c), key)
	MonitorLocalClip(w, key)
}

// Connect to the server (which starts a new clipboard), reconnecting
// automatically if the connection drops while the server is still up.
func ConnectToServer(address string) {
	for {
		if !connectOnce(address) {
			return
		}
		time.Sleep(3 * time.Second)
	}
}

// connectOnce dials the server and runs the clipboard sync until either
// direction of the connection fails, then returns true if the caller should
// reconnect or false if it should give up (e.g. plaintext connection dropped).
func connectOnce(address string) bool {
	c, err := net.Dial("tcp4", address)
	if c == nil {
		handleError(err)
		fmt.Println("Could not connect to", address)
		return true
	}
	if err != nil {
		handleError(err)
		return true
	}
	enableKeepAlive(c)

	key, err := resolveConnectionKey(c, false, address)
	if err != nil {
		handleError(err)
		_ = c.Close()
		return true
	}
	fmt.Printf("Connected to the clipboard at %s\n", address)

	done := make(chan struct{})
	go func() {
		MonitorSentClips(bufio.NewReader(c), key)
		close(done)
	}()
	MonitorLocalClip(bufio.NewWriter(c), key)
	_ = c.Close()
	<-done

	if key != nil {
		fmt.Printf("Connection to %s lost. Reconnecting...\n", address)
		return true
	}
	fmt.Printf("Connection to %s lost (unencrypted). Reconnecting without encryption is unsafe.\n", address)
	fmt.Println("Use -k or -s for secure reconnections. Exiting.")
	return false
}

// monitors for changes to the local clipboard and writes them to w
func MonitorLocalClip(w *bufio.Writer, key []byte) {
	for {
		mu.Lock()
		localClipboard = getLocalClip()
		mu.Unlock()
		debug("localClipboard changed. localClipboard =", localClipboard)
		err := sendClipboard(w, localClipboard, key)
		if err != nil {
			handleError(err)
			return
		}
		for localClipboard == getLocalClip() {
			time.Sleep(time.Second * time.Duration(secondsBetweenChecksForClipChange))
		}
	}
}

// monitors for clipboards sent through r
func MonitorSentClips(r *bufio.Reader, key []byte) {
	var foreignClipboard string
	var foreignClipboardBytes []byte
	for {
		err := gob.NewDecoder(r).Decode(&foreignClipboardBytes)
		if err != nil {
			if err == io.EOF {
				return // no need to monitor: disconnected
			}
			handleError(err)
			continue // continue getting next message
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
		// hacky way to prevent empty clipboard TODO: find out why empty cb happens
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

	err = gob.NewEncoder(w).Encode(clipboardBytes)
	if err != nil {
		return err
	}
	debug("sent:", clipboard)
	return w.Flush()
}

// Thanks to https://bruinsslot.jp/post/golang-crypto/ for crypto logic
func encrypt(key, data []byte) ([]byte, error) {
	key, salt, err := deriveKey(key, nil)
	if err != nil {
		return nil, err
	}
	blockCipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blockCipher)
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
	blockCipher, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blockCipher)
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
		if _, err = exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-out", "-selection", "clipboard")
		} else if _, err = exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--output", "--clipboard")
		} else if _, err = exec.LookPath("wl-paste"); err == nil {
			cmd = exec.Command("wl-paste", "--no-newline")
		} else if _, err = exec.LookPath("termux-clipboard-get"); err == nil {
			cmd = exec.Command("termux-clipboard-get")
		} else {
			handleError(errors.New("sorry, clipport won't work if you don't have xsel, xclip, wayland or Termux installed :(\nyou can create an issue at https://github.com/tsyche/clipport/issues"))
			os.Exit(2)
		}
	}
	if out, err = cmd.Output(); err != nil {
		handleError(err)
		return "An error occurred while getting the local clipboard"
	}
	if runtime.GOOS == "windows" {
		return strings.TrimSuffix(string(out), "\r\n") // powershell's get-clipboard adds a windows newline to the end for some reason
	}
	return string(out)
}

func getLocalClip() string {
	return runGetClipCommand()
}

func setLocalClip(s string) {
	var copyCmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		copyCmd = exec.Command("pbcopy")
	case "windows":
		copyCmd = exec.Command("clip")
	default:
		if _, err := exec.LookPath("xclip"); err == nil {
			copyCmd = exec.Command("xclip", "-in", "-selection", "clipboard")
		} else if _, err = exec.LookPath("xsel"); err == nil {
			copyCmd = exec.Command("xsel", "--input", "--clipboard")
		} else if _, err = exec.LookPath("wl-copy"); err == nil {
			copyCmd = exec.Command("wl-copy")
		} else if _, err = exec.LookPath("termux-clipboard-set"); err == nil {
			copyCmd = exec.Command("termux-clipboard-set")
		} else {
			handleError(errors.New("sorry, clipport won't work if you don't have xsel, xclip, wayland or Termux:API installed :(\nyou can create an issue at https://github.com/tsyche/clipport/issues"))
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
		fmt.Println("Disconnected")
	} else {
		fmt.Fprintln(os.Stderr, "error: ["+err.Error()+"]")
	}
}

func debug(a ...interface{}) {
	if printDebugInfo {
		fmt.Println("verbose:", a)
	}
}
