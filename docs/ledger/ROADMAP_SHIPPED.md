# Shipped Roadmap Items

**TL;DR:** Compact index of completed roadmap work followed by the original entries.

## Shipped index

- [x] 2026-06-23 — TCP keepalive/reconnect + flexible client addressing + keypair encryption
- [x] 2026-06-23 — `CLIPPORT_SECRET` env var + plaintext confirmation gate
- [x] 2026-06-24 — CI workflow running `go test -race ./...`
- [x] 2026-09-22 — Harden wire protocol against oversized/malformed frames
- [x] 2026-09-22 — Networking/crypto test coverage (TOFU, handshake, monitors, fuzz seed)
- [x] 2026-09-22 — Root-cause empty-clipboard workaround (sender no longer emits empty frames)
- [x] 2026-09-23 — Windows CRLF normalization on clipboard reads (uniclip#36)
- [x] 2026-09-23 — Clipboard read-error dedupe for non-text content (uniclip#23)
- [x] 2026-09-23 — Prefer Wayland wl-paste/wl-copy when $WAYLAND_DISPLAY set (uniclip#26)
- [x] 2026-09-23 — `clipport known-hosts` list/remove subcommand
- [x] 2026-09-23 — Exponential reconnect backoff (3s→30s) + permanent -k mismatch stop
- [x] 2026-09-23 — Multi-OS CI matrix (ubuntu/windows/macOS)
- [x] 2026-09-23 — IPv6 / dual-stack support (tcp4 → tcp, bracketed client addrs)

## Archived entries

### 2026-09-23 — IPv6 / dual-stack support

Top 3 item 1. Server binds with `net.Listen("tcp", …)` (was `tcp4`) and clients dial with `net.Dial("tcp", …)`, so IPv6-only and dual-stack LANs connect. `resolveClientAddress` builds addresses with `net.JoinHostPort`, accepting bracketed (`[fe80::1]:53701`), bare (`::1 -p 53701`), and zone-qualified forms; the printed join command brackets IPv6 addresses. IPv4 behavior unchanged; covered by `TestResolveClientAddress` IPv6 cases and `TestDualStackListenDial`.

### 2026-09-23 — `clipport known-hosts` list/remove subcommand

Top 3 item 1. `clipport known-hosts` (or `list`) prints trusted `-k` peers with fingerprints sorted by peer ID; `clipport known-hosts remove <peer>` deletes the entry from `~/.clipport/known_peers`. The TOFU mismatch warning now says to run that command instead of hand-editing the file (same idea as `ssh-keygen -R`).

### 2026-09-23 — Exponential reconnect backoff + permanent -k mismatch stop

Top 3 items 2–3, shipped together as one reconnect-hardening slice.

- `ConnectToServer` doubles the delay between failed `connectOnce` attempts: 3s → 6s → … → 30s cap (`nextBackoff`). A successful session resets the delay to 3s.
- Handshake failures that cannot succeed by retrying (local key mismatch, peer rejection) are wrapped in `permanentError`; `connectOnce` prints a fix hint (`clipport known-hosts remove <peer>`) and returns `retry=false` instead of looping forever. Dial failures and mid-session drops remain transient (`retry=true`).

### 2026-09-23 — Multi-OS CI matrix (ubuntu/windows/macOS)

Top 3 candidate (New Suggestions 2026-09-23). `test.yml` job now uses `strategy.matrix.os` of `ubuntu-latest`, `windows-latest`, `macos-latest` with `fail-fast: false` so `normalizeWindowsClip` and platform clipboard tests run on real runners. Tests set both `HOME` and `USERPROFILE` via `setTestHome` so `os.UserHomeDir` is isolated on Windows.

### 2026-06-23 — TCP keepalive/reconnect + flexible client addressing + keypair encryption

**Reconnect + TCP keepalive** — client auto-reconnects on a dropped connection instead of exiting; keepalive enabled on every connection to catch NAT/firewall idle timeouts.

**Per-device keypair encryption** — `-k`/`--key` mode: `clipport keygen` generates an X25519 keypair, connections derive a shared secret via ECDH, peers trusted-on-first-connect (`~/.clipport/known_peers`) with a loud abort on key mismatch.

**Flexible client addressing** — client accepts `host -p port` as an alternative to `host:port`.

Commits: `1557d3e`, `d582d67`.

### 2026-06-23 — `CLIPPORT_SECRET` env var + plaintext confirmation gate

`CLIPPORT_SECRET` skips the `--secure` password prompt when set. Connecting without `-s` or `-k` warns and requires confirmation (`Continue? [y/N]`).

### 2026-06-24 — CI workflow running `go test -race ./...`

Added `.github/workflows/test.yml`; the suite gates every push/PR to `main` instead of only running when someone remembers `just test` locally.

Commit: `e146d65`.

### 2026-09-22 — Harden wire protocol against oversized/malformed frames

`gob` decode in `MonitorSentClips` had no message-size cap — a malicious or buggy peer could send an unbounded payload. Added `maxClipboardFrameBytes` (8 MiB): per-frame `io.LimitedReader` on decode (disconnect on cap hit; stream desynced), send-side reject via `errClipboardTooLarge` before encode.

Regression tests: oversized send rejected, oversized frame disconnects uncleanly, valid frame + EOF still clean.

Wire-protocol hardening item from ROADMAP Top 3; work started under commit `53c724c`, completed and tested 2026-09-22.

### 2026-09-22 — Networking/crypto test coverage (TOFU, handshake, monitors, fuzz seed)

Top 3 item 3. Added production seams (`getLocalClip`/`setLocalClip` package vars, `generateKeypair` extraction, `MonitorLocalClip` stop channel, `peersMu`) and a ~800-line suite covering address/key resolution, ECDH handshake, TOFU trust store, `HandleClient`, `connectOnce`, both monitors, and `keygen`. Statement coverage 11.3% → 55%.

Tests found and fixed two real bugs while landing:

- **Multi-frame gob drop** — `MonitorSentClips` created a fresh `gob.Decoder` per loop iteration; buffered bytes from the prior decoder were discarded, so coalesced multi-frame streams lost every frame after the first. Fixed by reusing one decoder with a per-frame `LimitedReader.N` reset.
- **Decode-error spin** — non-EOF decode errors (`io.ErrUnexpectedEOF` is not `*net.OpError`) hit `handleError` + `continue` and looped forever on a dead connection. Decode failures now disconnect.

Also fixed while racing under `-race`: `HandleClient` only joined one of two monitor goroutines; `connectOnce` never stopped `MonitorLocalClip` on unclean shutdown (deadlock); `MonitorLocalClip` re-read `localClipboard` outside the mutex; `verifyOrTrustPeer` RMW'd `listOfClients` unlocked.

Includes `FuzzMonitorSentClips` seed corpus (fuzz-item suggestion largely satisfied).

### 2026-09-22 — Root-cause empty-clipboard workaround

Top 3 item 2. The `// hacky way to prevent empty clipboard TODO` dated to upstream `7daedea` (2022). Empty frames were produced because `MonitorLocalClip` always sent `getLocalClip()`, which returns `""` when the clipboard is cleared, at startup, or when the OS has no text type (macOS `pbpaste` on an image/file). Applying that would wipe the peer.

Fix: `MonitorLocalClip` no longer puts empty frames on the wire; `MonitorSentClips` still drops empty payloads from older peers. Intentional clear and non-text content remain non-propagating (text-only by design). Tests: empty not sent; empty→non-empty still sends; receive-side skip retained.

### 2026-09-23 — Windows CRLF normalization on clipboard reads

Top 3 item 1. Port of upstream [uniclip#36](https://github.com/quackduck/uniclip/pull/36) for [uniclip#35](https://github.com/quackduck/uniclip/issues/35): PowerShell `Get-Clipboard` rewrites every LF as CRLF and appends a trailing CRLF; `runGetClipCommand` only trimmed the trailing sequence, so internal CRLFs corrupted multi-line text on receiving peers.

Fix: extracted pure `normalizeWindowsClip` — `strings.ReplaceAll(s, "\r\n", "\n")` then trim one trailing `\n` — called on the Windows read path. Unit tests cover trailing CRLF, internal CRLF, LF-only input, lone CR, idempotence.

### 2026-09-23 — Clipboard read-error dedupe for non-text content

Top 3 item (error-spam). Upstream [uniclip#23](https://github.com/quackduck/uniclip/issues/23): with an image (or other non-text) on the clipboard, `runGetClipCommand` called `handleError` every poll (~1/s) and returned the sentinel `"An error occurred while getting the local clipboard"`, which `MonitorLocalClip` then put on the wire — peers pasted that string as text.

Fix: first failure in a streak logs once with a suppress-until-success note (`clipReadErrReported` atomic latch); subsequent failures silent; failed reads return `""` so nothing is sent (consistent with empty-frame policy). Reset on any successful read.

### 2026-09-23 — Prefer Wayland wl-paste/wl-copy when session is Wayland

Top 3 item (Wayland backend). Upstream [uniclip#26](https://github.com/quackduck/uniclip/issues/26): `linux` get/set preferred `xclip` whenever present; on Wayland with xclip installed, xclip fails `exit status 1`.

Fix: `linuxClipboardCommand` — when `$WAYLAND_DISPLAY` is set, try `wl-paste`/`wl-copy` first, then the historical order (xclip, xsel, wl-\*, termux) as fallback. X11 sessions without `WAYLAND_DISPLAY` keep xclip-first. Tests: fake PATH binaries for Wayland-first, X11 order, missing-wl fallback, no-tools error.
