# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

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
