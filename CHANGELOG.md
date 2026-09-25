# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- `clipport doctor`: read-only diagnostics that turn first-run failures into
  named checks — clipboard backend on PATH (Wayland/X11/macOS/Windows), state
  dir, keypair presence + fingerprint, trusted `known-hosts` count, whether a
  server is running, and listener reachability (dial a running server on
  loopback, or test-bind the `-p` port / an ephemeral port). Exits 1 when any
  check fails.
- GIF and BMP image sync alongside PNG and JPEG, plus WebP acceptance
  (WebP is decoded and re-encoded to PNG first — no platform clipboard or
  writer codec stores WebP natively). macOS reads the pasteboard in the
  order `clipboard info` lists types (raw/source first, then conversions)
  so an animated GIF is not downgraded to its PNG conversion, and uses
  `«class BMP »` (space-padded fourcc) because `BMPf` errors with -1700;
  Linux takes `--mime-type` from the sniffed format; Windows converts
  WebP before System.Drawing writes. Fingerprints are normalized through
  a pixel decode, so the same pixels in different formats do not echo
  back to the sender. Missing decoders or tools degrade to "no image" —
  never a crash.
- `clipport status` reports a stale-prune counter: the cumulative number
  of write-dead peers closed by the stale-probe passes (wake resume,
  full-server joiners), each of which reclaimed a slot ahead of TCP
  keepalive. Printed only when non-zero.
- `clipport key rotate`: regenerates this device's `-k` keypair safely —
  the old `key`/`key.pub` are renamed to timestamped `.bak` paths (or
  restored on failure), a fresh pair is generated, old and new
  fingerprints print, and a reminder covers the peer side: each peer must
  `clipport known-hosts remove <this-host>` after verifying the new
  fingerprint out of band. Before this, rotating meant manually deleting
  `~/.clipport/key` because `keygen` refuses to overwrite.
- `--password-file <path>` for `-s`: reads the shared password from a file
  (trailing newline stripped) so scripted deployments neither embed it in a
  command line nor prompt for it. Joins the `CLIPPORT_SECRET` /
  `CLIPPORT_PASSWORD` environment sources; file and environment are mutually
  exclusive and a misconfiguration fails loudly instead of silently using the
  wrong secret.
- `CLIPPORT_ALLOW_PLAINTEXT=1`: headless opt-in that skips the
  `Continue? [y/N]` prompt before connecting in plaintext (exact value
  only; anything else keeps the prompt). A one-line warning prints
  instead, so scripted plaintext deployments start unattended — pairs
  with `--quiet`.
- `clipport key fingerprint`: re-prints this device's own key
  fingerprint for out-of-band verification (`keygen` refuses to
  overwrite an existing key, and `known-hosts list` shows peers only).
- Oversize-frame graceful degradation: a clipboard payload over the 8 MiB
  wire cap no longer drops the sender's connection. Images are re-encoded
  on a downscale × JPEG-quality ladder to fit (headroom for gob/AES-GCM
  overhead); anything still over the cap is skipped with one warning per
  streak instead of failing the link (which previously made peers
  reconnect and potentially resend in a loop).
- Image clipboard support (PNG/JPEG): frames stay gob-encoded bytes with
  magic-sniffing on the receive side, so the wire shape is unchanged. Text
  reads fall back to platform image capture (`osascript` class data on
  macOS, `xclip`/`wl-paste` MIME targets on Linux, PowerShell
  `Get-Clipboard -Format Image` on Windows); applies via the matching
  setter. Image-to-image change detection compares pixel fingerprints so
  lossless re-encodes do not echo between peers.
- CI fuzz job: `FuzzMonitorSentClips` now runs its actual fuzzing loop for
  60s on every push/PR (`just fuzz` locally, same shape), not just the seed
  corpus — catches gob-decode regressions the static seeds miss. On
  failure, any crasher Go wrote under `testdata/fuzz/…` is uploaded as a
  workflow artifact (`fuzz-crashers`, 14-day retention) so the corpus can
  be committed and the bug reproduced locally.
- Readme "Security model" section: per-mode threat coverage for
  plaintext vs `-s` (scrypt password) vs `-k` (X25519 TOFU), including
  what plaintext does not protect and the limits of trust-on-first-use.
- `--max-clients N`: caps concurrent server connections (default 8,
  0 = unlimited). Overflow peers are closed with a server-side log
  line; pending handshakes count against the cap too. `clipport
status` shows the limit as `Clients (n/max)`.

### Changed

- Prune on full: when a joiner arrives at a full server, one stale-probe
  pass runs before the rejection, so `--max-clients` slots held by
  write-dead peers free immediately and healthy joiners are rarely
  rejected. Probe passes are rate-limited: one per 5s window no matter
  how many joiners are rejected (a wake-triggered pass refreshes the
  same window), so a flood of joiners cannot serialize probe writes
  under the clipboard lock.
- Server wake stale-slot pruning: after the server machine suspends and
  resumes, it probes every connected peer with an invisible empty frame
  and closes the write-dead ones, so their `--max-clients` slots free
  promptly instead of waiting out TCP keepalive (minutes) while a
  returning peer is rejected as "server full".
- Local lint parity: `just lintci` runs the same golangci-lint,
  prettier, markdownlint, textlint, shfmt, and actionlint checks
  super-linter pins in CI, so lint failures are caught before push.
- End-to-end loopback test: in-process `makeServer` ↔ `ConnectToServer`
  over a real loopback socket covers startup sync, change propagation,
  clean FIN shutdown, and the empty-server grace exit.
- Sleep/wake recovery: the client detects a suspend/resume as a large
  wall-clock gap in its clipboard poll loop, tears the stale connection
  down, and redials immediately (skipping reconnect backoff) instead of
  waiting minutes for TCP keepalive probes to time out. The server delays
  its last-client exit by a short grace period (10s) so that redial is
  not raced by the shutdown; a reconnecting client or pending handshake
  cancels the exit.
- Clipboard changes are debounced (250ms quiet window): a burst of
  rapid edits sends one frame carrying the final value instead of a
  frame per observed change. The initial startup snapshot still sends
  immediately.
- State-directory override: keys and `known_peers` live in `~/.clipport`
  unless `CLIPPORT_DIR` is set or `--dir` is passed (flag wins over env,
  env over default). Helps containers, CI, and multi-profile setups.
- `--quiet`/`-q`: suppresses status chatter (connect/trust/reconnect/
  shutdown lines) for headless launchd/systemd use. Errors on stderr,
  interactive prompts, and security warnings still print.
- `clipport status`: reports the running server's pid, port, connected
  clients, and when a clipboard frame was last pushed. Served over a
  Unix socket in the state directory (`clipport.sock`, mode 0600) —
  local-only query path, no auth needed, exits 1 when no server runs.
- `clipport known-hosts` subcommand: `list` (default) shows trusted `-k`
  peers with fingerprints; `remove <peer>` deletes an entry so key rotation
  no longer requires hand-editing `~/.clipport/known_peers`. The TOFU
  mismatch warning now points at this command.
- Client reconnect uses exponential backoff (3s → 30s cap) instead of a
  fixed 3s loop. The delay resets after a successful session.
- Permanent `-k` failures (local key mismatch, peer rejection) no longer
  reconnect forever: `connectOnce` stops and prints a fix hint instead.
- CI test job runs on a matrix of `ubuntu-latest`, `windows-latest`, and
  `macos-latest` so platform-specific clipboard/normalization paths are
  exercised on real runners.
- Networking/crypto test suite covering TOFU peer trust, ECDH handshake,
  `HandleClient` cleanup, `connectOnce` dial/shutdown, `MonitorLocalClip`,
  `MonitorSentClips`, and `keygen`; `FuzzMonitorSentClips` seed corpus.
  Statement coverage 11.3% → 55%.

### Fixed

- IPv6 / dual-stack support: the server now binds with `net.Listen("tcp", …)`
  (was `tcp4`) and clients dial with `net.Dial("tcp", …)`, so IPv6-only or
  dual-stack LANs can connect. Client addresses accept bracketed
  (`[fe80::1]:53701`), bare (`::1 -p 53701`), and zone-qualified forms;
  the printed join command uses `net.JoinHostPort` so IPv6 addresses are
  bracketed. IPv4 behavior is unchanged.
- Non-text clipboard content (e.g. an image) no longer floods stderr with
  `exit status 1` every poll: the first read failure in a streak is logged
  with a hint, further failures are suppressed until a read succeeds, and
  the failed read returns `""` instead of an error sentinel so peers never
  receive the message as clipboard text (uniclip#23).
- On Wayland sessions (`$WAYLAND_DISPLAY` set), `wl-paste`/`wl-copy` are
  preferred over `xclip`/`xsel`, so a Wayland machine that also has xclip
  installed uses the Wayland-native tool instead of failing with
  `exit status 1` (uniclip#26).
- Windows clipboard reads no longer corrupt multi-line text: PowerShell
  `Get-Clipboard` rewrites every LF as CRLF and appends a trailing CRLF; only
  the trailing sequence was trimmed before, so internal CRLFs reached peers
  (uniclip#35 / uniclip#36). All CRLFs are now normalized to LF before the
  trailing newline is stripped.
- Empty-clipboard workaround root-caused and tightened: `getLocalClip` returns
  `""` for a cleared clipboard, at startup, and when the OS clipboard has no
  text type (e.g. macOS `pbpaste` on an image). `MonitorLocalClip` no longer
  puts empty frames on the wire; `MonitorSentClips` still drops empty frames
  from older peers so they cannot wipe this device's clipboard. Intentional
  clear and non-text content still do not propagate (text-only by design).
- `MonitorSentClips` created a fresh `gob.Decoder` per frame, discarding bytes
  the previous decoder had already buffered — subsequent frames on a coalesced
  stream were silently dropped. Now one decoder for the stream with a per-frame
  size-cap reset.
- `MonitorSentClips` treated non-EOF decode errors as recoverable and looped
  forever on a dead connection (`io.ErrUnexpectedEOF` is not a `*net.OpError`).
  Decode failures now disconnect.
- `HandleClient` only waited for one of its two monitor goroutines before
  returning, leaking the other past function exit (race under `-race`).
- `connectOnce` never stopped `MonitorLocalClip` on unclean shutdown, deadlocking
  the reconnect path.
- `MonitorLocalClip` re-read `localClipboard` outside the mutex (data race).
- `verifyOrTrustPeer` read-modify-wrote `listOfClients` without a lock (TOFU path).

### Security

- Wire protocol now caps clipboard frames at 8 MiB: oversized gob payloads
  disconnect the peer instead of allocating unboundedly; send side rejects
  frames over the limit before encoding

## [0.1.7] - 2026-07-02

### Fixed

- Server no longer hangs indefinitely with a dead client still in its list: cleanup now
  triggers on whichever side (read or write) detects the dropped connection first, instead
  of only on the next local clipboard change

## [0.1.6] - 2026-07-01

### Changed

- On disconnect, the server now removes the client from its active list and
  exits once no clients remain connected, instead of continuing to run
- Suppressed noisy error logging for expected network disconnects (timeouts,
  broken pipe, connection reset) on peer drop

## [0.1.5] - 2026-06-30

### Added

- Server catches SIGINT/SIGTERM and propagates shutdown to connected clients
  (OS-level TCP FIN), so clients exit cleanly instead of trying to reconnect

### Changed

- `MonitorSentClips` now signals clean vs. unclean disconnect so clients can
  distinguish a deliberate server shutdown from a dropped connection

## [0.1.4] - 2026-06-28

### Changed

- ROADMAP housekeeping (mark completed install-docs item done)

## [0.1.3] - 2026-06-28

### Changed

- `README`: per-OS install instructions (macOS, Linux, NixOS, Windows,
  Android/Termux, source), uninstall table, Nix run/profile install

## [0.1.2] - 2026-06-28

### Changed

- `README`: numbered install options (Homebrew, binary, source); brew uninstall step
- `CHANGELOG`: backfilled 0.1.0/0.1.1 entries; corrected reconnect claim to secure-only
- CLAUDE.md/AGENTS.md: documented `just install`; fixed architecture reconnect description
- ROADMAP.md: removed completed Homebrew tap item

## [0.1.1] - 2026-06-28

### Added

- Homebrew tap (`brew tap tsyche/tap && brew install clipport`)
- `just install` recipe to build and install binary to `/usr/local/bin`
- Prerelease test step in GitHub Actions release workflow

### Changed

- Plaintext connections now exit on drop instead of reconnecting, with a message
  suggesting `-k` or `-s`; secure connections (`-k`/`-s`) still reconnect automatically
- Server distinguishes secure vs plaintext peers on drop: secure peers get a
  re-verification notice, plaintext peers get a warning they cannot be re-admitted

## [0.1.0] - 2026-06-28

### Added

- `-k`/`--key` keypair encryption mode: `clipport keygen` generates a per-device X25519
  key, connections derive a shared secret via ECDH, and peers are trusted-on-first-connect
  (`~/.clipport/known_peers`)
- `CLIPPORT_SECRET` environment variable to supply the `--secure` password without a prompt
- `host -p port` as an alternative to `host:port` for the client address
- Confirmation prompt before connecting without `-s` or `-k` (plaintext warning)
- `-p`/`--port` flag to pin the listen port instead of randomizing
- GitHub Actions CI (tests, lint, CodeQL) and goreleaser release workflow

### Changed

- Client auto-reconnects on a dropped connection (secure mode) instead of exiting
- TCP keepalive enabled on all connections to detect NAT/firewall idle timeouts

### Fixed

- Client never actually used a password in `--secure` mode
