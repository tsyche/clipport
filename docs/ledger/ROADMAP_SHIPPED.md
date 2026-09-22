# Shipped Roadmap Items

**TL;DR:** Compact index of completed roadmap work followed by the original entries.

## Shipped index

- [x] 2026-06-23 — TCP keepalive/reconnect + flexible client addressing + keypair encryption
- [x] 2026-06-23 — `CLIPPORT_SECRET` env var + plaintext confirmation gate
- [x] 2026-06-24 — CI workflow running `go test -race ./...`
- [x] 2026-09-22 — Harden wire protocol against oversized/malformed frames

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

`gob` decode in `MonitorSentClips` had no message-size cap — a malicious or buggy peer could send an unbounded payload. Added `maxClipboardFrameBytes` (8 MiB): per-frame `io.LimitedReader` on decode (disconnect on cap hit; stream desynced), send-side reject via `errClipboardTooLarge` before encode. Regression tests: oversized send rejected, oversized frame disconnects uncleanly, valid frame + EOF still clean.

Wire-protocol hardening item from ROADMAP Top 3; work started under commit `53c724c`, completed and tested 2026-09-22.
