# Shipped Roadmap Items

**TL;DR:** Compact index of completed roadmap work followed by the original entries.

## Shipped index

- [x] 2026-06-23 — TCP keepalive/reconnect + flexible client addressing + keypair encryption
- [x] 2026-06-23 — `CLIPPORT_SECRET` env var + plaintext confirmation gate
- [x] 2026-06-24 — CI workflow running `go test -race ./...`
- [x] 2026-09-22 — Harden wire protocol against oversized/malformed frames
- [x] 2026-09-22 — Networking/crypto test coverage (TOFU, handshake, monitors, fuzz seed)
- [x] 2026-09-22 — Root-cause empty-clipboard workaround (sender no longer emits empty frames)

## Archived entries

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

Top 3 item 3. Added production seams (`getLocalClip`/`setLocalClip` package vars, `generateKeypair` extraction, `MonitorLocalClip` stop channel, `peersMu`) and a ~800-line suite covering address/key resolution, ECDH handshake, TOFU trust store, `HandleClient`, `connectOnce`, both monitors, and `keygen`. Statement coverage 11.3% → 53%.

Tests found and fixed two real bugs while landing:

- **Multi-frame gob drop** — `MonitorSentClips` created a fresh `gob.Decoder` per loop iteration; buffered bytes from the prior decoder were discarded, so coalesced multi-frame streams lost every frame after the first. Fixed by reusing one decoder with a per-frame `LimitedReader.N` reset.
- **Decode-error spin** — non-EOF decode errors (`io.ErrUnexpectedEOF` is not `*net.OpError`) hit `handleError` + `continue` and looped forever on a dead connection. Decode failures now disconnect.

Also fixed while racing under `-race`: `HandleClient` only joined one of two monitor goroutines; `connectOnce` never stopped `MonitorLocalClip` on unclean shutdown (deadlock); `MonitorLocalClip` re-read `localClipboard` outside the mutex; `verifyOrTrustPeer` RMW'd `listOfClients` unlocked.

Includes `FuzzMonitorSentClips` seed corpus (fuzz-item suggestion largely satisfied).

### 2026-09-22 — Root-cause empty-clipboard workaround

Top 3 item 2. The `// hacky way to prevent empty clipboard TODO` dated to upstream `7daedea` (2022). Empty frames were produced because `MonitorLocalClip` always sent `getLocalClip()`, which returns `""` when the clipboard is cleared, at startup, or when the OS has no text type (macOS `pbpaste` on an image/file). Applying that would wipe the peer.

Fix: `MonitorLocalClip` no longer puts empty frames on the wire; `MonitorSentClips` still drops empty payloads from older peers. Intentional clear and non-text content remain non-propagating (text-only by design). Tests: empty not sent; empty→non-empty still sends; receive-side skip retained.
