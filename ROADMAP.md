# Roadmap

Inferred from the codebase on 2026-06-16 (no prior ROADMAP.md existed); audited and updated 2026-06-23; audited 2026-09-22. Single-file Go app — see `clipport.go`, `AGENTS.md`/`CLAUDE.md` for architecture. Shipped history: [`docs/ledger/ROADMAP_SHIPPED.md`](docs/ledger/ROADMAP_SHIPPED.md).

## Top 3 Suggested Tasks

1. **Endless error spam on non-text clipboard content** — ~1-2 hrs (promoted; see Inherited #3)
   - `runGetClipCommand` `handleError`s every poll when the clipboard has no readable text (related to the empty-cb work just shipped); rate-limit/dedupe or warn once.
2. **Wayland + xclip picks the wrong backend** — ~1 hr (promoted; see Inherited #2)
   - Prefer `$WAYLAND_DISPLAY` + `wl-paste`/`wl-copy` before falling back to `xclip`.

## Inherited from upstream (quackduck/uniclip) — triaged 2026-06-16

Checked the upstream repository's open issues against this fork's actual code (not just assumed carried over):

1. **Windows clipboard CRLF corruption** — shipped 2026-09-23; see `docs/ledger/ROADMAP_SHIPPED.md`. Upstream [uniclip#35](https://github.com/quackduck/uniclip/issues/35) / [uniclip#36](https://github.com/quackduck/uniclip/pull/36).
2. **Wayland + xclip picks the wrong backend** — promoted to Top 3 item 2
   `runGetClipCommand`/`setLocalClip` check `xclip` before `wl-paste`/`wl-copy`.
   On a Wayland session that happens to have `xclip` installed, clipport silently fails with `exit status 1` instead of using the Wayland-native tool.
   Upstream: [uniclip#26](https://github.com/quackduck/uniclip/issues/26). Fix: check `$WAYLAND_DISPLAY` first and prefer `wl-paste`/`wl-copy` when set. ~1 hr.
3. **Endless error spam on non-text clipboard content** — promoted to Top 3 item 1
   When the system clipboard holds something `pbpaste`/`xclip`/etc. can't read as text (e.g. an image), `runGetClipCommand` calls `handleError` and returns a sentinel string every poll cycle, forever, with no backoff or one-time warning.
   Upstream: [uniclip#23](https://github.com/quackduck/uniclip/issues/23). Fix: rate-limit/dedupe the error or warn once and skip until clipboard content type changes. ~1-2 hrs.

Lower priority / not clearly actionable yet:

- **"use of closed network connection" after Windows hibernation** ([uniclip#32](https://github.com/quackduck/uniclip/issues/32)) — reporter couldn't reliably reproduce; revisit if it recurs for us.
- Custom-port feature request ([uniclip#20](https://github.com/quackduck/uniclip/issues/20)) is already done in this fork via `-p`/`--port`.

## New Suggestions (2026-06-23)

1. **`clipport known-hosts` management subcommand** — ~1-2 hours
   - Right now the only way to remove a stale/rotated peer from `~/.clipport/known_peers` is to hand-edit the file (the mismatch warning even says so). A small `list`/`remove <peer>` subcommand would make key rotation usable without asking users to edit a text file by hand — same idea as `ssh-keygen -R`.
2. **Exponential backoff on client reconnect** — ~30-60 minutes
   - `ConnectToServer`'s retry loop currently retries every fixed 3 seconds forever. Fine for a brief network blip, but if the server is down for an extended period (or a `-k` peer mismatch is permanent) this spams reconnect attempts and log output indefinitely. Cap with backoff (e.g. 3s → 30s) instead.
3. **Distinguish permanent vs. transient reconnect failures** — ~1 hour
   - Related to the above: a `-k` peer-mismatch rejection is permanent (will never succeed by retrying) but currently gets the same "Reconnecting..." treatment as a transient network drop. Worth a distinct code path that stops retrying and tells the user to fix the key mismatch instead of looping.

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
- **Test coverage for new networking/crypto code** — shipped 2026-09-22 (Top 3 item 3); see `docs/ledger/ROADMAP_SHIPPED.md`.
- **`flake.nix` `vendorSha256` staleness check** — unverified against current `go.mod`/`go.sum` since the rebrand; likely fine but not confirmed.

## Notes

- This roadmap was bootstrapped from `TODO` comments, recent Git history, and reading `clipport.go` directly — there was no prior roadmap or stated long-term vision to preserve. Revisit priorities once real users/usage patterns emerge.
- 2026-09-22 audit: shipped wire-protocol frame cap (Top 3 item 1); demoted Windows CRLF to new Top 3 #1 as lowest-effort high-impact fix; added fuzz-item suggestion (approved).
- 2026-09-22: shipped test-coverage item (Top 3 #3); fixed multi-frame gob decode bug found by tests; promoted Wayland backend order to new Top 3 #3; empty-cb line number refreshed (`clipport.go:646`).
- 2026-09-22: shipped empty-clipboard root-cause (Top 3 #2): empty frames no longer emitted from `MonitorLocalClip`; receive-side drop kept as backstop for older peers; `TODO` comment removed.
- 2026-09-23: shipped Windows CRLF normalization (Top 3 #1, port of uniclip#36): `normalizeWindowsClip` maps all `\r\n` → `\n` then trims the trailing newline; Top 3 now lists error-spam (#1) and Wayland backend order (#2).
