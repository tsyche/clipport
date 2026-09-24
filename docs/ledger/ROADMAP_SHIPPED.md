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
- [x] 2026-09-23 — `CLIPPORT_DIR` env override + `--dir` flag
- [x] 2026-09-23 — `--quiet`/`-q` flag (errors-only headless mode)
- [x] 2026-09-23 — `clipport status` subcommand (unix-socket status query)
- [x] 2026-09-23 — Max-clients cap (`--max-clients`, slot accounting)
- [x] 2026-09-23 — Clipboard change debounce (250ms quiet window)
- [x] 2026-09-23 — CLI security model section (`README` threat coverage)
- [x] 2026-09-23 — Sleep/wake dead-peer recovery (wake gap → instant redial)
- [x] 2026-09-24 — CI fuzz job (`FuzzMonitorSentClips`, 60s loop)
- [x] 2026-09-24 — PNG/JPEG image clipboard sync
- [x] 2026-09-24 — Local lint parity script (`just lintci`)
- [x] 2026-09-24 — Inherited upstream triage closed (uniclip#20, uniclip#32)
- [x] 2026-09-24 — Oversize-frame graceful degradation (shrink or skip, never drop link)
- [x] 2026-09-24 — Server wake stale-slot pruning (empty-frame probe, write-dead close)
- [x] 2026-09-24 — `clipport key fingerprint` subcommand (own-key TOFU verification)
- [x] 2026-09-24 — End-to-end loopback integration test (sync/propagation/FIN/grace exit)
- [x] 2026-09-24 — `CLIPPORT_ALLOW_PLAINTEXT` headless opt-in (skip plaintext prompt)
- [x] 2026-09-24 — Prune stale clients when server is full before rejecting

## Archived entries

### 2026-09-24 — Prune on full before rejecting

1. **Prune on full before rejecting** — ~20 minutes
   - When a joiner hits "server full", run one stale-probe pass first: write-dead slots (e.g. a peer that never came
     back after a previous wake) free immediately, so healthy peers rarely get rejected. Follow-on from shipped server
     wake pruning. _(Promoted to Top 3.)_

Shipped: `reserveClientSlotWithPrune` now fronts the `makeServer`
accept loop — when `tryReserveClientSlot` fails (server at
`--max-clients`), one `pruneStale` pass runs, then a bounded poll
(20 × 25ms) waits for `HandleClient`'s async cleanup to release the
freed slot before retrying the reservation; only if no slot opens is
the joiner rejected with the existing "server full" log line. Exactly
one probe pass runs per rejected joiner, so a flood of joiners cannot
turn the accept loop into continuous probing. Tests:
`TestReserveClientSlotSkipsPruneWhenNotFull`,
`TestReserveClientSlotPrunesWhenFull` (a `releasingProbeConn` stands
in for HandleClient's slot release), and
`TestReserveClientSlotStillFullAfterPrune` (prunes exactly once, still
rejects). Commit: `9939cb5`; Test + Lint + CodeQL green in CI.

### 2026-09-24 — `CLIPPORT_ALLOW_PLAINTEXT` headless opt-in

1. **Headless plaintext opt-in** — ~30 minutes
   - `--quiet` is aimed at launchd/systemd, but plaintext mode still blocks on an interactive `Continue? [y/N]` prompt.
     An explicit `CLIPPORT_ALLOW_PLAINTEXT=1` env opt-in would let scripted plaintext deployments start unattended
     without weakening the default gate. _(Promoted to Top 3.)_

Shipped: `plaintextOptIn()` gates the `confirmPlaintext()` call in
`main` — when `CLIPPORT_ALLOW_PLAINTEXT=1` (exact match only; anything
else keeps the prompt), a one-line warning prints instead of the
`Continue? [y/N]` prompt and startup proceeds. Verified by CLI smoke
tests on all three paths (unset → prompt+abort on EOF, `=1` → starts
with warning, `=true` → still prompts) plus `TestPlaintextOptIn` for
the value gate. Documented in CLI help and the `README` encryption
section. Commit: `c509d32`; Test + Lint + CodeQL green in CI.

### 2026-09-24 — End-to-end loopback integration test

1. **End-to-end loopback integration test** — ~2 hours
   - Unit tests cover monitors, handshake, and cleanup in isolation, but nothing exercises `makeServer` ↔
     `ConnectToServer` over a real loopback socket end-to-end: startup snapshot sync, clipboard change propagation, and
     Ctrl+C/FIN shutdown semantics. An in-process end-to-end test would lock the full path down. Promoted from
     2026-09-24 suggestions — worth locking down after today's image/oversize churn.

Shipped: `TestEndToEndLoopback` runs `makeServer` and `ConnectToServer`
in one process over a real loopback socket (encrypted `-s`): startup
snapshot sync, clipboard change propagation through the debounce window,
clean EOF shutdown (client exits instead of reconnecting — FIN proof;
Ctrl+C's `os.Exit(0)` handler is untestable in-process, so FIN only), and
the empty-server grace exit via stubbed `exitProcess`. Seams added:
`isClientProcess` skips the receive-side re-broadcast on clients (a no-op
across real processes, prevents the in-process test from echoing frames
forever), and a `runningServer` handle exposes the live listener plus
wake-watcher stop/join (`wakeStop`/`wakeDone`) so tests shut the accept
loop down deterministically. In-process limitation (documented in-test):
both sides share one clipboard, so `setLocalClip` is record-only in the
test and direction-specific behavior stays with the unit tests. 10/10
`-race` runs green. Commit: `0a8b0f7`; Test + Lint + CodeQL green in CI.

### 2026-09-24 — `clipport key fingerprint` subcommand

1. **Show own key fingerprint** — ~30 minutes
   - The security model tells users to verify fingerprints out of band, but after `clipport keygen` there is no way to
     re-print _your own_ fingerprint (`known-hosts list` shows trusted peers only). A `clipport key fingerprint` (or a
     line in `clipport status`) closes the TOFU verification loop. Promoted from 2026-09-24 suggestions.

Shipped: `clipport key fingerprint` dispatches through
`runKeyCommand` → `ownFingerprint`, which loads the device keypair and
prints `fingerprint(pub)` — byte-identical to what `clipport keygen`
printed at generation time. Missing key errors with a pointer to
`clipport keygen` (exit 1); bare/unknown `key` subcommands print usage.
Documented in CLI help (usage + example) and `README` (usage, example,
security-model paragraph). Tests: `TestOwnFingerprintMatchesKeygen`,
`TestOwnFingerprintMissingKey`, `TestRunKeyCommandUsageErrors`.
Commit: `7a2be58`; Test + Lint + CodeQL green in CI.

### 2026-09-24 — Server wake stale-slot pruning

1. **Server wake stale-slot pruning** — ~1-2 hours
   - After the _server_ machine resumes from sleep, dead client entries hold `--max-clients` slots until TCP keepalive
     eventually fails them (minutes); a returning peer can be rejected as "server full" the whole time. Prune
     write-dead/stale clients promptly on server resume — without closing live connections, which would deliver a clean
     EOF that healthy clients treat as server shutdown and exit (the failure mode deliberately avoided in the shipped
     sleep/wake work).

Shipped: `watchServerWake` samples the server wall clock every second and,
after a suspend gap ≥ `wakeGapThreshold` plus a short settle for network
reassociation, runs `pruneStaleClients`: each listed peer gets one empty
clipboard frame (`sendClipboard(cl.w, "", cl.key)`) under a 3s write
deadline — a frame receivers already discard (`MonitorSentClips` drops
empty payloads), so healthy peers absorb it invisibly. Write-failed peers
are closed so `HandleClient`'s existing cleanup frees the `--max-clients`
slot; live peers are never closed (that clean EOF would make them exit),
and black-holed-but-writable peers fall back to the existing TCP keepalive
detection. `monitorLocalClip` now sends under `mu`, serializing probes
with clipboard sends — which also fixes the pre-existing dual-writer race
on shared `bufio.Writer`s. Tests: `TestPruneStaleClientsClosesWriteDeadPeer`,
`TestPruneStaleClientsKeepsLivePeer`, `TestPruneStaleClientsSkipsUnlistedClient`,
`TestPruneStaleClientsLeavesLiveHandleClientRunning`,
`TestWatchServerWakeTriggersPrune`. Commit: `6b89ca9`; Test + Lint + CodeQL green in CI.

### 2026-09-24 — Oversize-frame graceful degradation (shrink or skip, never drop link)

1. **Oversize-image graceful degradation** — ~1 hour
   - A screenshot PNG over the 8 MiB frame cap (`maxClipboardFrameBytes`) fails in `sendClipboard`, which breaks that
     client's connection instead of skipping the frame; peers then reconnect and may loop on the same image. Downscale/
     re-encode to fit (or skip with a single log line) so huge captures never drop the link. Follow-on from shipped
     image clipboard support.

Shipped: `monitorLocalClip` now sends through `sendFrame` — an oversize
image is re-encoded on a downscale × JPEG-quality ladder
(`shrinkImageToFit` + dependency-free `downscaleBox`, with headroom under
the cap for gob/AES-GCM overhead) and retried; anything still over the cap
(non-image text, or an image no ladder can fit) is skipped with one warning
per streak (`oversizeFrameReported` latch, cleared on the next successful
send) instead of failing the connection. Tests: `TestShrinkImageToFit`,
`TestSendFrameSkipsOversizeTextWithoutError`, `TestSendFrameShrinksOversizeImage`,
`TestMonitorLocalClipSurvivesOversizeFrame` (shared `oversizePNG` fixture).
Commits: `cc2bbff`, CI fixes `2518c9a`; Test + Lint green in CI.

### 2026-09-24 — Inherited upstream triage closed (uniclip#20, uniclip#32)

Original Inherited-from-upstream section (triaged 2026-06-16), preserved:

Lower priority / not clearly actionable yet:

- **"use of closed network connection" after Windows hibernation** ([uniclip#32](https://github.com/quackduck/uniclip/issues/32)) — reporter couldn't reliably reproduce; revisit if it recurs for us.
- Custom-port feature request ([uniclip#20](https://github.com/quackduck/uniclip/issues/20)) is already done in this fork via `-p`/`--port`.

Resolutions (2026-09-24):

- **uniclip#20** — closed as shipped in this fork: `-p`/`--port` pins the
  listen port (`clipport.go` flag registration, `README` intro, `CHANGELOG`).
- **uniclip#32** — closed as not-reproducible + covered by shipped work: the
  sleep/wake dead-peer recovery (wake-gap detection → instant redial, 2026-09-23),
  exponential reconnect backoff (3s→30s, `nextBackoff`), and
  `isNetworkDisconnect` handling mean a stale connection after resume tears
  down and redials cleanly instead of erroring. Reopen only if the exact
  upstream error recurs here.

### 2026-09-24 — Local lint parity script (`just lintci`)

1. **Local lint parity script** — ~1 hour
   - The local machine lacks golangci-lint / prettier / textlint in PATH, so super-linter is the first line of defense
     and CI failures cost a full push-wait cycle (three consecutive red lint runs in the 2026-09-24 session alone). A
     `just lintci` (or setup addition) installing pinned versions matching CI would catch GO/PRETTIER/textlint
     categories before push.

Shipped: `just lintci` reproduces super-linter v7.1.0 locally with pinned
versions — golangci-lint 1.60.3 (cached binary install, `GOTOOLCHAIN=go1.23.12`
because 1.60.3 cannot read newer export data), prettier 3.3.3, markdownlint-cli
0.41.0, textlint 14.2.0 (+ `textlint-rule-terminology`,
`textlint-filter-rule-comments`). Added `.golangci.yml` and `.textlintrc.json`
matching super-linter's TEMPLATES so config is identical on both sides;
lintci's prettier globs also cover JSON files and `.github` YAML/JSON (the
JSON_PRETTIER category that caught the first attempt). Documented in
`AGENTS.md`/`CLAUDE.md`/`CONTRIBUTING.md`. Verified green locally and in CI.
Commits: `2293434`, `5a076d1`.

### 2026-09-24 — PNG/JPEG image clipboard sync

1. **Image/binary clipboard support** — ~1 day
   - Text-only by design today: non-text content (e.g. macOS `pbpaste` on an image) returns `""` and never reaches peers; wire frames are `string`-oriented. Upstream users have asked for image paste (uniclip#23 comment thread); extension needs a wire-format change (length-prefixed bytes or type-tagged frames) and platform-native read/write for PNG/JPEG (and possibly files).
   - 🧑 needs-human: scope decision — images only, images+files, or full multi-format MIME

Shipped with scope decided: **images only (PNG/JPEG)** — no wire-format change
needed after all: frames were already gob-encoded `[]byte`, and the receiver
sniffs magic bytes (no type tag). Text reads fall back to platform image
capture (`osascript` `«data PNGf/JPEGf»` on macOS, `xclip`/`wl-paste` MIME
targets on Linux, PowerShell `Get-Clipboard -Format Image` on Windows);
applies via the matching setter. Image-to-image change detection compares
pixel fingerprints (decode → deterministic PNG re-encode → sha256) so
lossless re-encodes do not echo between peers. Tests: `TestIsImagePayload`,
`TestClipboardStateChangedImageRoundtrip`, `TestParseOsascriptData`,
`TestMonitorLocalClipSendsImagePayload`, `TestMonitorSentClipsAppliesImagePayload`,
`TestRunSetClipCommandRoutesImageToWriter`. Follow-on risk filed as roadmap
item: oversize images (>8 MiB frame cap) currently drop the sender link.
Commits: `e2222e8`, lint fixes `2c77492`; Test + Lint green in CI.

### 2026-09-24 — CI fuzz job (`FuzzMonitorSentClips`, 60s loop)

Top 3 item 1. `FuzzMonitorSentClips` seed corpus exists but CI only runs the seed (`go test` without `-fuzz`). A short
`-fuzz` job (e.g. 60s per PR) would catch decode regressions the static corpus misses.

Shipped: `test.yml` gains a `fuzz` job (ubuntu-only) running
`go test -race -run=^$ -fuzz=FuzzMonitorSentClips -fuzztime=60s .` on every push/PR alongside the existing matrix;
`just fuzz` runs the same command locally. Verified green in CI (run `35943418159`, job "Fuzz MonitorSentClips (60s)")
on commit `de8427e`; local 10s smoke run passed with no crashers. Commit: `de8427e`.

### 2026-09-23 — Sleep/wake dead-peer recovery (wake gap → instant redial)

Top 3 item 1. Both sides rely on TCP keepalive (30s period, several missed probes) to notice a vanished peer — this can take minutes after wake before either side reacts. Detecting the local machine's own wake (e.g. macOS `NSWorkspace` notifications, or a large wall-clock gap between poll iterations) and immediately probing/closing stale connections would make recovery near-instant instead of "eventually."

Shipped: client-side wall-clock gap detection — `monitorLocalClip(..., checkWake=true)` treats a poll iteration spanning
`wakeGapThreshold` (30s) as suspend/resume, latches `systemWoke`, and tears the connection down; `ConnectToServer` sees
the latch and redials immediately instead of honoring backoff. Server-side monitors never wake-detect
(`MonitorLocalClip` wrapper passes `checkWake=false`): closing live server connections would deliver a clean EOF that
healthy clients mistake for server shutdown and exit permanently. The server also delays its last-client exit by a 10s
grace (`exitIfStillEmptyAfter`, cancelled by a reconnecting client or a pending handshake) so the instant redial is not
raced by process shutdown. Tests: `TestMonitorLocalClipWakeGapLatchesAndReturns`,
`TestMonitorLocalClipWithoutWakeCheckNeverReadsClock`, `TestExitIfStillEmptyAfterExitsWhenEmpty`,
`TestExitIfStillEmptyAfterStaysWhenClientPresent`, `TestExitIfStillEmptyAfterStaysWhileHandshakePending`. Commit: `24807d5`.

### 2026-09-23 — CLI security model section (`README` threat coverage)

Top 3 item 1. Plaintext vs `-s` (scrypt password) vs `-k` (X25519 TOFU) had scattered explanations across install docs and
CLI prompts. A single "Security model" section spelling out the threat each mode addresses (and what plaintext does _not_
protect) sets expectations before someone pastes secrets over a LAN. Shipped as a `README` `## Security model` section after
`## Encryption`: per-mode coverage (confidentiality/integrity/identity for each of plaintext, `-s`, `-k`), the TOFU
first-contact caveat, and the explicit non-goals (compromised local device, no internet exposure). Docs-only — no tests.

### 2026-09-23 — Clipboard change debounce (250ms quiet window)

Top 3 item 1. `MonitorLocalClip` polls and sends on every observed change; rapid multi-line edits or a large
terminal dump can put many frames on the wire before the receiver pastes. Debouncing (e.g. 100–300ms quiet
window, send only the latest snapshot) cuts churn and avoids peers receiving intermediate states they never
see locally. Shipped as a 250ms quiet window (`waitClipboardQuiet`, 50ms poll): the startup snapshot still
sends immediately; every later change coalesces to one frame with the final value. Covered by
`TestMonitorLocalClipDebouncesRapidChanges` (burst `[one two three four]` → frames `[one four]`).

### 2026-09-23 — Max-clients cap (`--max-clients`, slot accounting)

Top 3 item 1. Plaintext mode already warns and requires confirmation before joining, but the server never bounds
how many peers attach; anyone who can reach the port can keep connecting. A `--max-clients N` flag (sensible
default, e.g. 8) limits accidental exposure and log noise without adding real auth — pairs with the
plaintext-transport-security backlog item as a stopgap, not a replacement. Shipped as default 8, `0` =
unlimited; `activeConns` counts pending handshakes too (`tryReserveClientSlot`/`releaseClientSlot`), overflow
closed with a server-side reject log, `clipport status` shows `Clients (n/max)`. Covered by
`TestTryReserveClientSlotEnforcesCap`, `TestTryReserveClientSlotUnlimitedWhenZero`, `TestCurrentStatusSnapshot`.

### 2026-09-23 — `clipport status` subcommand (unix-socket status query)

Top 3 item 1. The server exposes a local Unix socket (`<statedir>/clipport.sock`, mode 0600) via
`startStatusServer`/`serveStatus`; `clipport status` dials it (`queryStatus`) and prints pid, listen
port, connected client addresses, and time since the last clipboard push (`lastClipPush`, set by
`MonitorLocalClip` after a successful send). Local-only query path — no auth needed, dead server =
dial failure with exit 1; direct subcommand output is not gated by `--quiet`. Covered by
`TestCurrentStatusSnapshot`, `TestServeStatusRoundtrip`, `TestQueryStatusNoServer`,
`TestRunStatusNoServerErrorIsNotSilent`.

### 2026-09-23 — `--quiet`/`-q` flag (errors-only headless mode)

Top 3 item 1. Status chatter (start/join/connect/trust/reconnect/shutdown/EOF-disconnect) routes through `info`/`infof`, suppressed by `--quiet`/`-q`. Errors on stderr, interactive prompts, security warnings (TOFU mismatch, plaintext drop), and direct subcommand output (keygen, known-hosts) always print. Covered by `TestInfoRespectsQuiet` and `TestHandleErrorQuietStillPrintsErrors`.

### 2026-09-23 — `CLIPPORT_DIR` env override + `--dir` flag

Top 3 item 1. `clipportDir()` resolves state (keys, `known_peers`) as: `--dir` flag, then `$CLIPPORT_DIR`, then `~/.clipport`, creating the directory with 0700. Helps containers, CI, and multi-profile setups; `setTestHome` now clears both overrides so ambient env cannot leak into tests. Covered by `TestClipportDirEnvOverride`, `TestClipportDirFlagBeatsEnv`, `TestClipportDirDefaultHome`.

### 2026-09-23 — IPv6 / dual-stack support

Top 3 item 1. Server binds with `net.Listen("tcp", …)` (was `tcp4`) and clients dial with
`net.Dial("tcp", …)`, so IPv6-only and dual-stack LANs connect. `resolveClientAddress` builds
addresses with `net.JoinHostPort`, accepting bracketed (`[fe80::1]:53701`), bare (`::1 -p 53701`),
and zone-qualified forms; the printed join command brackets IPv6 addresses. IPv4 behavior unchanged;
covered by `TestResolveClientAddress` IPv6 cases and `TestDualStackListenDial`.

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
