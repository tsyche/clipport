# Roadmap

Inferred from the codebase on 2026-06-16 (no prior ROADMAP.md existed); audited and updated 2026-06-23; audited 2026-09-22; audited 2026-09-23; audited 2026-09-24; audited 2026-09-25. Single-file Go app — see `clipport.go`, `AGENTS.md`/`CLAUDE.md` for architecture. Shipped history: [`docs/ledger/ROADMAP_SHIPPED.md`](docs/ledger/ROADMAP_SHIPPED.md).

## Top 3 Suggested Tasks

1. **CI `goreleaser check`** — ~20 minutes
   - `.goreleaser.yml` is only validated at tag push (`VALIDATE_GO_RELEASER` is disabled in `linter.yml` because
     super-linter's goreleaser v2.2.0 rejects `formats`), so release-config breakage surfaces on release day instead
     of on push; a pinned `goreleaser check` step in CI closes the documented gap. _(Promoted to Top 3.)_
2. **`--json` output for status/doctor** — ~40 minutes
   - Both subcommands print human-oriented lines only; a `--json` mode would let launchd/systemd health checks and
     scripts parse pid/clients/prune counts and doctor results instead of scraping text. _(Promoted to Top 3.)_
3. **Headless service docs (launchd/systemd)** — ~45 minutes, docs-only
   - The shipped `--quiet`/`--password-file`/`CLIPPORT_ALLOW_PLAINTEXT` trio targets launchd/systemd, but `README` has
     no unit files to copy; add working launchd plist + systemd service examples using them. _(Promoted to Top 3.)_

## New Suggestions (2026-07-02)

1. **Server re-announces or survives an IP change after reassociation** — ~half day, needs design
   - The connect string (`clipport <ip>:<port>`) is printed once at startup. If the server's Wi-Fi reassociates after sleep and gets a new DHCP lease, that printed IP goes stale and clients get "could not connect" with no indication why.
   - Options: periodically re-announce the current IP, or move to mDNS/Bonjour-style discovery instead of a static printed address (may overlap with Remote Connectivity below).

## New Suggestions (2026-09-24)

1. **File-path clipboard sync** — ~half day
   - Copying a file in Finder/Explorer puts a path/URI on the clipboard, not bytes; sync the path text so the peer pastes a usable location (same-machine paths aside).
   - 🧑 needs-human: scope decision — what a cross-OS path should paste as (POSIX vs Windows path, or an error)

## New Suggestions (2026-09-25)

1. **State-dir migration helper** — ~45 minutes
   - Moving to a new machine means hand-copying `~/.clipport` (key + `known_peers`) or redoing TOFU with every peer;
     a documented `clipport` flow (or `clipport key export`/`import`) would move trust state safely.
2. **Dependabot for Go modules + Actions** — ~15 minutes
   - No automated dependency-update PRs; a minimal `.github/dependabot.yml` (gomod + `github-actions` ecosystems, weekly)
     keeps `x/image`/`x/sys` and action pins current without manual bumps.
3. **Extend loopback end-to-end test to image payloads** — ~45 minutes
   - `TestEndToEndLoopback` proves text sync over a real socket but never sends an image; stage a PNG (and GIF) through
     `getLocalClip`/`applied` to prove image frames cross the wire and land through `setLocalClip` end-to-end.

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
- **`flake.nix` `vendorSha256` staleness check** — unverified against current `go.mod`/`go.sum` since the rebrand, and `go.mod` changed again on 2026-09-25 (`golang.org/x/image` added), so the hash is almost certainly stale until `nix build` re-fetches it (no `nix` binary on the dev machine to recompute).

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
- 2026-09-23: shipped `CLIPPORT_DIR` env override + `--dir` flag (Top 3 #1). New Top 3: quiet/log-level, status/peers, max-clients (promoted from 2026-09-23 suggestions).
- 2026-09-23: shipped `--quiet`/`-q` errors-only mode (Top 3 #1). New Top 3: status/peers, max-clients, debounce (promoted from 2026-09-23 suggestions).
- 2026-09-23: shipped `clipport status` subcommand (Top 3 #1, unix-socket query path). New Top 3: max-clients, debounce, CLI security-model docs (promoted from 2026-09-23 suggestions).
- 2026-09-23: shipped max-clients cap (Top 3 #1, `--max-clients` default 8 / 0=unlimited, pending-handshake slot accounting). New Top 3: debounce, CLI security-model docs, sleep/wake recovery (promoted from 2026-07-02 suggestions). No new suggestions (2026-09-23 batch already approved).
- 2026-09-23: shipped clipboard debounce (Top 3 #1, 250ms quiet window in `waitClipboardQuiet`; startup snapshot unsent-delayed). New Top 3: CLI security-model docs, sleep/wake recovery, fuzz CI wiring (promoted from 2026-09-22 suggestions). No new suggestions.
- 2026-09-23: shipped CLI security model section (Top 3 #1, `README` `## Security model` threat coverage; docs-only). New Top 3: sleep/wake recovery, fuzz CI wiring, image clipboard 🧑 needs-human scope decision (promoted from 2026-09-23 suggestions — Top 3 not fully blocked: first two agent-doable). No new suggestions.
- 2026-09-24: shipped sleep/wake dead-peer recovery (Top 3 #1: client wake-gap → instant redial, server 10s disconnect grace). New Top 3: fuzz CI wiring, image clipboard 🧑 scope decision, server wake slot pruning (promoted from new suggestions). Approved two more new suggestions (local lint parity, end-to-end loopback test).
- 2026-09-24: shipped fuzz CI wiring (Top 3 #1: 60s `FuzzMonitorSentClips` job + `just fuzz`). New Top 3: image clipboard 🧑 scope decision, server wake slot pruning, local lint parity (promoted). Approved three new suggestions (own-key fingerprint, headless plaintext opt-in, fuzz-failure artifact upload); end-to-end loopback test stays in suggestions.
- 2026-09-24 audit: shipped image clipboard (Top 3 #1, scope decided images-only PNG/JPEG — moved to ledger). Promoted oversize-image graceful degradation to Top 3 #3 (regression risk from shipped images: >8 MiB frames drop the sender link). Approved three new suggestions (GIF/BMP/WebP formats, status payload kind, file-path sync 🧑).
- 2026-09-24 audit: shipped local lint parity (Top 3 #2: `just lintci` + super-linter-matching configs, commits `2293434`/`5a076d1`). Promoted own-key fingerprint from 2026-09-24 suggestions. New Top 3: server wake pruning, oversize-image degradation, own-key fingerprint — all agent-doable. No new suggestions.
- 2026-09-24: closed out the last Inherited-from-upstream items (uniclip#20 custom port — already shipped as `-p`/`--port`; uniclip#32 Windows-hibernation disconnect — covered by shipped sleep/wake recovery, reconnect backoff, and `isNetworkDisconnect` handling, not reproducible here). Section removed; both entries archived in the ledger.
- 2026-09-24 audit: shipped oversize-frame graceful degradation (Top 3 #2: `sendFrame` shrink-or-skip, commits `cc2bbff`/`2518c9a` — moved to ledger). Promoted end-to-end loopback test from 2026-09-24 suggestions. New Top 3: server wake pruning, own-key fingerprint, end-to-end loopback — all agent-doable. No new suggestions.
- 2026-09-24 audit: shipped server wake stale-slot pruning (Top 3 #1: `watchServerWake` + `pruneStaleClients` empty-frame probe, commit `6b89ca9` — moved to ledger).
  Promoted headless plaintext opt-in from 2026-09-24 suggestions. New Top 3: own-key fingerprint, end-to-end loopback, headless plaintext opt-in — all agent-doable. Approved two new follow-on suggestions (prune-on-full, stale-prune count in status).
- 2026-09-24 audit: shipped own-key fingerprint (Top 3 #1: `clipport key fingerprint`, commit `7a2be58` — moved to ledger). Promoted prune-on-full from suggestions. New Top 3: end-to-end loopback, headless plaintext, prune-on-full — all agent-doable. Approved one new suggestion (key rotate helper).
- 2026-09-24 audit: shipped end-to-end loopback integration test (Top 3 #1: in-process `makeServer` ↔ `ConnectToServer` loopback
  covering startup sync, change propagation, FIN shutdown, grace exit; commit `0a8b0f7` — moved to ledger, along with the stale
  own-key fingerprint suggestion left behind by the previous audit). Promoted fuzz-failure artifact upload from suggestions.
  New Top 3: headless plaintext, prune-on-full, fuzz-failure artifact upload — all agent-doable. Approved three new suggestions
  (password env/file, `clipport doctor`, known-hosts last-seen).
- 2026-09-24 audit: shipped headless plaintext opt-in (Top 3 #1: `CLIPPORT_ALLOW_PLAINTEXT=1` gate in `main`, commit `c509d32` —
  moved to ledger). Promoted `CLIPPORT_PASSWORD`/`--password-file` from suggestions (companion to headless secure mode).
  New Top 3: prune-on-full, fuzz-failure artifact upload, password env/file — all agent-doable. No new suggestions (same-day
  batch of three still pending).
- 2026-09-24 audit: shipped prune-on-full (Top 3 #1: `reserveClientSlotWithPrune` runs one stale-probe pass before rejecting
  a full joiner, commit `9939cb5` — moved to ledger) and the approved CHANGELOG catch-up (shipped same-day, commit
  `5389489` — backfilled wake pruning, key fingerprint, plaintext opt-in, prune-on-full). Promoted prune-pass cooldown from
  this audit's suggestions. New Top 3: fuzz artifact upload, password env/file, prune-pass cooldown — all agent-doable.
  Declined (not written): server-full reason to joiner.
- 2026-09-24 audit: shipped fuzz-failure artifact upload (Top 3 #1: `upload-artifact` step gated on `failure()` in the CI
  fuzz job, commit `252539c` — moved to ledger). Promoted `clipport key rotate` from suggestions. New Top 3: password
  env/file, prune-pass cooldown, key rotate — all agent-doable. No new suggestions (six still pending from today's batches).
- 2026-09-25 audit: shipped all three Top 3 items from 2026-09-24 — `--password-file`/`CLIPPORT_SECRET` env pairing
  (`1a81f27`), prune-pass cooldown (`ade0dba`), `clipport key rotate` (`f1c0627`) — all moved to ledger. New Top 3:
  `clipport doctor`, GIF/BMP/WebP formats, stale-prune count — all agent-doable. Approved four new suggestions
  (CI goreleaser check, `--version` flag, headless service docs, state-dir migration helper). Workflow note (not a
  roadmap item): `just lintci` gained shfmt + actionlint (pinned v1.7.12) with the super-linter config resolved at
  `.github/linters/actionlint.yml`, and all workflows now pin `ubuntu-26.04` runners.
- 2026-09-25 audit: shipped all three Top 3 items — `clipport doctor` (`eef6f82`, Windows test fix `55904b4`),
  GIF/BMP/WebP image sync (`ed7b4d7`, macOS reads in `clipboard info` listed order + live roundtrip test),
  stale-prune count in status (`2dadda2`) — all moved to ledger. New Top 3: CI goreleaser check, status payload
  kind, known-hosts last-seen — all agent-doable. Removed the stale `--version` suggestion (flag + goreleaser
  ldflags already shipped). Approved three new suggestions (`--json` output, Dependabot, end-to-end image payloads).
- 2026-09-25: shipped status payload kind (Top 3 #2, `recordClipPush` kind+size in the pushed line, commit `dc13323`)
  and known-hosts last-seen (Top 3 #3, optional third column in `known_peers`, commit `820ad7b`) — both moved to
  ledger. Promoted `--json` output and headless service docs from suggestions; CI goreleaser check stays Top 3 #1.
