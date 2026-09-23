# Roadmap

Inferred from the codebase on 2026-06-16 (no prior ROADMAP.md existed); audited and updated 2026-06-23; audited 2026-09-22; audited 2026-09-23. Single-file Go app — see `clipport.go`, `AGENTS.md`/`CLAUDE.md` for architecture. Shipped history: [`docs/ledger/ROADMAP_SHIPPED.md`](docs/ledger/ROADMAP_SHIPPED.md).

## Top 3 Suggested Tasks

1. **`CLIPPORT_DIR` env override** — ~30-60 minutes
   - `clipportDir()` hardcodes `~/.clipport`; tests already patch `HOME`/`USERPROFILE` to work around it. An env var (and optional flag) pointing at an alternate state directory helps containers, CI, multi-profile setups, and makes TOFU tests cleaner.
2. **Quiet / log-level flags** — ~1 hour
   - Connect, trust, reconnect, and shutdown messages all print unconditionally; there is no way to run headless (launchd/systemd) with errors-only output, nor a `-v` mode that dumps monitor/poll debug. Add `--quiet` (errors only) and/or verbose debug beyond the existing `-d`.
3. **`clipport status` / peers subcommand** — ~1 hour
   - The server only prints a one-line trust/fingerprint message when each peer connects; there is no on-demand way to list currently connected clients, their peer IDs, or when clipboard content last changed. A `status` (or `peers`) subcommand — local IPC or a tiny query path on the existing port — would make multi-device setups debuggable without attaching a debugger.

## Inherited from upstream (quackduck/uniclip) — triaged 2026-06-16

Checked the upstream repository's open issues against this fork's actual code (not just assumed carried over). Completed items moved to `docs/ledger/ROADMAP_SHIPPED.md`.

Lower priority / not clearly actionable yet:

- **"use of closed network connection" after Windows hibernation** ([uniclip#32](https://github.com/quackduck/uniclip/issues/32)) — reporter couldn't reliably reproduce; revisit if it recurs for us.
- Custom-port feature request ([uniclip#20](https://github.com/quackduck/uniclip/issues/20)) is already done in this fork via `-p`/`--port`.

## New Suggestions (2026-07-02)

1. **Sleep/wake detection to speed up dead-peer recovery** — ~2-3 hours
   - Both sides rely on TCP keepalive (30s period, several missed probes) to notice a vanished peer — this can take minutes after wake before either side reacts.
   - Detecting the local machine's own wake (e.g. macOS `NSWorkspace` notifications, or a large wall-clock gap between poll iterations) and immediately probing/closing stale connections would make recovery near-instant instead of "eventually."
2. **Server re-announces or survives an IP change after reassociation** — ~half day, needs design
   - The connect string (`clipport <ip>:<port>`) is printed once at startup. If the server's Wi-Fi reassociates after sleep and gets a new DHCP lease, that printed IP goes stale and clients get "could not connect" with no indication why.
   - Options: periodically re-announce the current IP, or move to mDNS/Bonjour-style discovery instead of a static printed address (may overlap with Remote Connectivity below).

## New Suggestions (2026-09-22)

1. **Fuzz wire decode path** — ~1 hour — largely done
   - `FuzzMonitorSentClips` seed corpus landed with the 2026-09-22 test-coverage push; optional follow-up is wiring a short `go test -fuzz` run into CI.

## New Suggestions (2026-09-23) — approved

1. **Image/binary clipboard support** — ~1 day
   - Text-only by design today: non-text content (e.g. macOS `pbpaste` on an image) returns `""` and never reaches peers; wire frames are `string`-oriented. Upstream users have asked for image paste (uniclip#23 comment thread); extension needs a wire-format change (length-prefixed bytes or type-tagged frames) and platform-native read/write for PNG/JPEG (and possibly files).
   - 🧑 needs-human: scope decision — images only, images+files, or full multi-format MIME
2. **CLI security model section** — ~1 hour
   - Plaintext vs `-s` (scrypt password) vs `-k` (X25519 TOFU) have scattered explanations across install docs and CLI prompts. A single "Security model" section spelling out the threat each mode addresses (and what plaintext does _not_ protect) would set expectations before someone pastes secrets over a LAN.
3. **Max-clients / connection cap on server** — ~30-60 minutes
   - Plaintext mode already warns and requires confirmation before joining, but the server never bounds how many peers attach; anyone who can reach the port can keep connecting. A `--max-clients N` flag (sensible default, e.g. 8) limits accidental exposure and log noise without adding real auth — pairs with the plaintext-transport-security backlog item as a stopgap, not a replacement.
4. **Clipboard change debounce / coalesce** — ~1 hour
   - `MonitorLocalClip` polls and sends on every observed change; rapid multi-line edits or a large terminal dump can put many frames on the wire before the receiver pastes. Debouncing (e.g. 100–300ms quiet window, send only the latest snapshot) cuts churn and avoids peers receiving intermediate states they never see locally.

## Remote Connectivity (Cross-Network)

Long-term feature area — requires `-k` mode only, local network operation unchanged.

### Architecture: Pluggable Transport Interface

Define a `Transport` interface so relay implementations can be swapped or chained without rewriting core logic. Fallback chains (try libp2p → fall back to relay) become trivial. Design this interface before building any specific transport.

```sh
clipport --remote                          # use default transport
clipport --remote --transport=relay        # explicit relay
clipport --remote --transport=libp2p,relay # libp2p with relay fallback
```

Security constraint: remote mode must require `-k`; clear error message if attempted without it. Plaintext over a relay is explicitly blocked.

### Transport Options (roughly in implementation order)

1. **Self-hosted relay on Fly.io** — simplest first step; deploy a small Go relay service, devices get short human-readable IDs (e.g. `pine-cloud-42`), relay sees only encrypted ciphertext. Free tier sufficient for personal use. ~1-2 days.

2. **Nostr signaling** — use the existing Nostr relay network for peer discovery/rendezvous. Publish a signed "available" event with your public key, other side reads it and initiates connection. No account, no central authority, keypair model maps directly to clipport's existing `-k` infrastructure. Multiple public relays provide redundancy. ~2-3 days.

3. **libp2p transport** — battle-tested NAT traversal, distributed relay discovery, no central coordinator. Used by IPFS and Ethereum. Most mature decentralized option but highest implementation complexity. ~1 week.

4. **Waku signaling** — purpose-built decentralized messaging layer from the Ethereum/Status ecosystem. Privacy-preserving metadata design, censorship resistant. Similar role to Nostr but with stronger privacy guarantees and a growing dedicated relay network. ~3-5 days.

### Notes

- No blockchain transaction layer (Bitcoin/Ethereum on-chain) — fees and latency make it a bad fit
- Nostr and Waku relay networks are the right layer to piggyback on, not the chains themselves
- Headscale (self-hosted Tailscale coordination server) is an option for users who want the Tailscale UX without the account dependency, but requires running a server — not meaningfully simpler than the relay approach

## Backlog

- **AUR package (Arch/Manjaro)** — goreleaser v2 has native `aurs` support; requires an AUR account, an SSH keypair, and the private key added as a GitHub Actions secret (`AUR_SSH_PRIVATE_KEY`). ~30 min once prerequisites are in place. 🧑 needs-human: AUR account + SSH key registration
- **Scoop bucket (Windows)** — goreleaser has native `scoops` support; create a `scoop-bucket` repository under `tsyche`, wire it up in `.goreleaser.yml` similarly to the Homebrew tap. ~20 min. 🧑 needs-human: create GitHub repository under account
- **Transport security for non-encrypted mode** — cleartext mode still has no authentication between peers; anyone who can reach the port can join the clipboard. The plaintext confirmation gate at least makes this an explicit, opt-in choice rather than a silent default — but the underlying gap (no auth) is unchanged.
- **`flake.nix` `vendorSha256` staleness check** — unverified against current `go.mod`/`go.sum` since the rebrand; likely fine but not confirmed.

## Notes

- This roadmap was bootstrapped from `TODO` comments, recent Git history, and reading `clipport.go` directly — there was no prior roadmap or stated long-term vision to preserve. Revisit priorities once real users/usage patterns emerge.
- 2026-09-22 audit: shipped wire-protocol frame cap (Top 3 item 1); demoted Windows CRLF to new Top 3 #1 as lowest-effort high-impact fix; added fuzz-item suggestion (approved).
- 2026-09-22: shipped test-coverage item (Top 3 #3); fixed multi-frame gob decode bug found by tests; promoted Wayland backend order to new Top 3 #3; empty-cb line number refreshed (`clipport.go:646`).
- 2026-09-22: shipped empty-clipboard root-cause (Top 3 #2): empty frames no longer emitted from `MonitorLocalClip`; receive-side drop kept as backstop for older peers; `TODO` comment removed.
- 2026-09-23: shipped Windows CRLF normalization (Top 3 #1, port of uniclip#36): `normalizeWindowsClip` maps all `\r\n` → `\n` then trims the trailing newline; Top 3 then listed error-spam (#1) and Wayland backend order (#2).
- 2026-09-23: shipped error-spam dedupe (uniclip#23) and Wayland-first backend pick (uniclip#26).
- 2026-09-23 audit: migrated Inherited #1–#3 (all shipped) out of active roadmap; removed shipped test-coverage backlog line; promoted known-hosts / backoff / permanent-vs-transient to Top 3; approved nine new suggestions (multi-OS CI, security-model docs, image clipboard [needs scope decision], IPv6, CLIPPORT_DIR, quiet/log-level, status/peers, max-clients, debounce).
- 2026-09-23: shipped known-hosts subcommand, reconnect backoff + permanent -k mismatch stop, and multi-OS CI matrix (old Top 3). New Top 3: IPv6, CLIPPORT_DIR, quiet/log-level.
- 2026-09-23: shipped IPv6 / dual-stack support (Top 3 #1): listen/dial switched `tcp4` → `tcp`, `resolveClientAddress` accepts bracketed/bare/zone IPv6 via `net.JoinHostPort`, printed join command bracketed. New Top 3: CLIPPORT_DIR, quiet/log-level, status/peers.
