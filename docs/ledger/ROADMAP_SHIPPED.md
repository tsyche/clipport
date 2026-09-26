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
- [x] 2026-09-24 — Fuzz-failure artifact upload (`testdata/fuzz/…` on CI failure)
- [x] 2026-09-25 — `--password-file` for `-s` secure mode (+ `CLIPPORT_SECRET` env path)
- [x] 2026-09-25 — Prune-pass cooldown on the accept loop (one probe pass per 5s)
- [x] 2026-09-25 — `clipport key rotate` subcommand (backup + regenerate)
- [x] 2026-09-25 — `clipport doctor` diagnostics subcommand
- [x] 2026-09-25 — GIF/BMP/WebP image format sync (WebP re-encoded to PNG)
- [x] 2026-09-25 — Stale-prune count in `clipport status`
- [x] 2026-09-25 — Status payload kind + size in `clipport status`
- [x] 2026-09-25 — Last-seen time in `known-hosts list`
- [x] 2026-09-25 — Server IP re-announce + LAN address discovery (UDP 33334)

## Archived entries

### 2026-09-25 — Server IP re-announce + LAN address discovery

1. **Server re-announces or survives an IP change after reassociation** — ~half day, needs design
   - The connect string (`clipport <ip>:<port>`) is printed once at startup. If the server's Wi-Fi reassociates after sleep and gets a new DHCP lease, that printed IP goes stale and clients get "could not connect" with no indication why.
   - Options: periodically re-announce the current IP, or move to mDNS/Bonjour-style discovery instead of a static printed address.

Shipped — both halves, no mDNS dependency (a fixed UDP probe keeps the zero-config story and adds no
library to a single-file binary):

- **Server re-announces.** `watchServerAddress` polls `outboundIPTo("8.8.8.8:80")` every
  `serverIPWatchInterval` (5s) from `makeServer` and, when the address differs from the last one
  seen, prints `Server network address changed: Run \`clipport <ip:port>\` to join this clipboard`.
  Silent while the lookup fails (no route during the drop), so a transient outage does not spam.
- **Clients survive it.** `ConnectToServer` runs `rediscoverServer` after every failed dial: it
  takes the port from the configured address and calls `discoveryProbe` (var seam, default
  `discoverServerAddress`) which broadcasts `clipport-discover` to every non-loopback interface's
  directed broadcast on UDP `33334`, then accepts only a `clipport-server <ip:port>` reply naming
  the same port (`parseDiscoveryReply` — a different clipport server must not hijack a client pinned
  elsewhere). A match prints `Found the clipboard server at <addr>` and redials it immediately,
  skipping the backoff; a miss logs a single hint per outage and retrying continues as before.
  The port is re-extracted on each attempt, so a discovered address stays discoverable.
- Server-side binds once in `makeServer` via `serveDiscovery` (stopped by a `discoveryStop` channel
  on return); a bind failure (UDP 33334 taken on the host) only disables discovery — the server
  still re-prints its address. `directedBroadcast` skips degenerate masks (/0, /32, non-canonical)
  and IPv6 (no directed broadcast — discovery is IPv4-only by design).

Design note: discovery only ever supplies an address to dial; it carries no clipboard bytes and no
peer identity, so `-k`/`-s`/plaintext rules are unchanged for the session that follows. Documented
as its own `README` bullet under Security model (unauthenticated reply reveals the server's address
to LAN probes; a won race can only stall a reconnect, not forge a trusted peer).

Tests (9 new): `TestDirectedBroadcast` mask table, `TestDiscoveryTargetsAvoidLoopback`,
`TestParseDiscoveryReply` (port mismatch / malformed), `TestDiscoverServerAddressOnFindsReply` +
`FindsNothing`, `TestServeDiscoveryRepliesToProbe` + `IgnoresUnknownPayloads`,
`TestWatchServerAddressAnnouncesChange` (announces once per change), and
`TestConnectToServerRediscoversChangedAddress` (injected `discoveryProbe`, full dial → moved
address → session completes without user intervention). `preserveGlobals` restores `discoveryProbe`.
Docs: `README` (Usage subsection + Security model bullet), `AGENTS.md`/`CLAUDE.md` architecture
note, CHANGELOG. Lint: gosec `G115` on the `int(fd)` `SO_BROADCAST` cast suppressed with a
`#nosec` comment (fd is kernel-supplied).

### 2026-09-25 — Last-seen time in `known-hosts list`

1. **Last-seen timestamp in `known-hosts list`** — ~30 minutes
   - After wake-prune work, users have no way to tell whether a trusted peer is still alive from the list alone;
     record and show a last-seen time per entry (updated on handshake). _(Promoted to Top 3.)_

Shipped: `known_peers` lines gained an optional third column
(`<id> <key> <unix-nano>`) — `loadKnownPeers` returns
`map[string]knownPeer{Key, LastSeen}` (legacy two-column lines parse
with zero = never), `saveKnownPeers` omits the column until a stamp
exists. `verifyOrTrustPeer` restamps on every successful key match
(save failure logs at debug and does not fail the handshake; a key
mismatch still aborts before stamping), so existing files migrate on
their next handshake. `listKnownPeers` prints
`last seen 2m30s ago` / `never` via `lastSeenLabel` + `labelNever`.
Help line + `README` (usage + security section) + CHANGELOG updated.
Tests: TOFU stamp assert, restamp + no-stamp-on-mismatch, 3-column
load/save roundtrip, `TestLastSeenLabel`, `TestListKnownPeersShowsLastSeen`.
Live smoke against a legacy file. Commit: `820ad7b`; Test + Lint +
CodeQL + Docs green in CI.

### 2026-09-25 — Status payload kind + size in `clipport status`

1. **`clipport status` shows last-synced payload kind** — ~30 minutes
   - `status` reports a timestamp only; add text vs image and byte size so users can confirm an image actually
     propagated without watching both terminals. _(Promoted to Top 3.)_

Shipped: `recordClipPush` stamps `lastClipKind`
(`clipKindText`/`clipKindImage`, derived by magic sniff) and
`lastClipBytes` alongside `lastClipPush` on every successfully pushed
frame (extracted from `monitorLocalClip` to keep gocyclo ≤ 15).
`statusSnapshot` gained `last_clip_kind`/`last_clip_bytes`;
`runStatus` prints `Clipboard last pushed: 9s ago (image, 41.0 KiB)`
and falls back to the plain timestamp line when the kind is empty
(older status server). `formatByteSize` helper (B/KiB/MiB/GiB).
Tests: monitor pushes assert kind+size for text and image payloads,
snapshot + serve-roundtrip coverage, `TestRunStatusPayloadLine`
(incl. fallback), `TestFormatByteSize`. Commit: `dc13323`; Test +
Lint + CodeQL + Docs green in CI.

### 2026-09-25 — Stale-prune count in `clipport status`

1. **Stale-prune count in `clipport status`** — ~20 minutes
   - Wake pruning only logs at debug level; surface a closed-by-prune counter in the status snapshot so users can see
     that slots were reclaimed. Follow-on from shipped server wake pruning, prune-on-full, and prune-pass cooldown.
     _(Promoted to Top 3.)_

Shipped: `prunedClients atomic.Int64` — incremented in
`pruneStaleClients` only when `conn.Close()` returns nil (a repeat
pass before `HandleClient` cleanup unlists the conn cannot
double-count, mirroring `net.Conn`'s error-on-second-close);
`statusSnapshot.Pruned` (`pruned` JSON field) flows through
`currentStatus`/`serveStatus`, and `runStatus` prints
`Stale clients pruned: N` only when N > 0 (older servers without
the field simply omit the line). Fake probe conns now error on
repeat close so tests match real net.Conn semantics. Help/README/
CHANGELOG updated. Tests: `TestCurrentStatusSnapshot`,
`TestServeStatusRoundtrip`, `TestRunStatusPruneLine`, prune-pass
tests extended. Commit: `2dadda2`; Test + Lint + CodeQL + Docs
green in CI.

### 2026-09-25 — GIF/BMP/WebP image format sync

1. **GIF/BMP/WebP image formats** — ~1-2 hours
   - Image sync currently sniffs PNG/JPEG only; extend magic bytes, osascript class data (`GIFf`, `BMPf`), xclip/wl
     MIME targets, and Windows fallbacks so animated GIFs and WebP screenshots propagate too. _(Promoted to Top 3.)_

Shipped: magic sniffing (`gifMagic`, `riffMagic`/`webpMagic`,
BMP DIB-header check) in `imagePayloadFormat`; write paths per
platform — darwin `darwinImageClass` (`GIFf`/`BMPf`/`PNGf`/
`JPEGf`), `linuxImageMIME` for `--mime-type`, Windows converts
WebP→PNG before System.Drawing. WebP decoded via pinned
`golang.org/x/image v0.30.0` (last go1.23-compatible version) +
`x/image/bmp`, both registered via blank imports; `webpToPNG`
re-encodes for platforms with no WebP codec. macOS reads types
in `clipboard info` LISTED order (raw/source first, then
conversions — a fixed preference order read GIF conversions of
PNGs) using `«class BMP »` (BMPf errors -1700 on read) with
label parsing (`GIF picture`/`JPEG picture`) and direct-probe
fallback; `imageFingerprint` normalizes through decode→RGBA→PNG
so cross-format echoes are prevented; `readLinuxImage` probes
gif/webp/bmp/png/JPEG order. Live opt-in test
`TestLiveDarwinImageRoundtrip` (`CLIPPORT_LIVE_CLIPBOARD=1`)
verifies gif/bmp/png/webp roundtrip + clipboard restore on macOS.
README/AGENTS/CLAUDE/CHANGELOG updated. Commit: `ed7b4d7`;
Test + Lint + CodeQL + Docs green in CI.

### 2026-09-25 — `clipport doctor` diagnostics subcommand

1. **`clipport doctor` diagnostics** — ~1 hour
   - First-run failures (missing clipboard backend, no key, firewall on the port) surface as opaque connect errors;
     a `clipport doctor` subcommand would check backend availability, keypair/known-hosts state, and listener
     reachability, pointing at the fix. _(Promoted to Top 3.)_

Shipped: `clipport doctor` → `runDoctor(port)` dispatches after
`status` in `main`; `collectDoctorChecks` builds the battery —
clipboard backend on PATH (`requireOnPATH` + `clipboardBackendDetail`
with Wayland/X11/macOS/Windows detail), state dir, `doctorKeypairCheck`
(keypair presence + fingerprint), trusted `known-hosts` count,
running-server check, and `doctorListenerCheck` (dial a running
server on loopback, else test-bind the `-p` port / an ephemeral
port). `doctorOK`/`doctorFail` statuses; exits 1 when any check
fails. Help + `README` usage blocks + CHANGELOG updated; Windows
test stubs corrected (`LookPath("clip")` resolves via PATHEXT →
`powershell.exe`/`clip.exe`). Tests: `TestClipboardBackendDetailMissingTools/ReportsTool`,
`TestCollectDoctorChecksNoServerAllOK/KeyPairPresent/ListenerBusy/RunningServer`.
Commits: `eef6f82` + `55904b4`; Test + Lint + CodeQL green in CI.

### 2026-09-25 — `clipport key rotate` subcommand

1. **`clipport key rotate` helper** — ~20 minutes
   - `keygen` refuses to overwrite an existing key, so rotating today means manually removing `~/.clipport/key`; a
     `clipport key rotate` subcommand would regenerate safely and remind the user to re-verify fingerprints with peers.
     Follow-on from shipped own-key fingerprint work. _(Promoted to Top 3.)_

Shipped: `clipport key rotate` dispatches through `runKeyCommand`
(usage now `clipport key fingerprint | clipport key rotate`) →
`rotateKey`: backs up `key`/`key.pub` to timestamped `.bak` files
(`<name>.<YYYYMMDD-HHMMSS>.bak`, restored with stderr warnings if
regeneration fails), regenerates the X25519 keypair, and prints old +
new fingerprints plus the `clipport known-hosts remove <this-host>`
reminder for peers. Documented in CLI help and `README` (usage blocks +
encryption section); CHANGELOG entry added. Tests: `TestKeyRotate`,
`TestKeyRotateBackups`, `TestKeyRotateMissingKey`,
`TestRunKeyCommandUsageErrors` (extended). Commit: `f1c0627`; Test +
Lint + CodeQL green in CI.

### 2026-09-25 — Prune-pass cooldown on the accept loop

1. **Prune-pass cooldown on the accept loop** — ~20 minutes
   - Follow-on to shipped prune-on-full: every rejected joiner triggers one stale-probe pass, so a flood of joiners at
     capacity serializes probe writes under the global clipboard lock. A shared minimum interval between passes keeps
     probe traffic bounded while preserving the slot-reclaim benefit. _(Promoted to Top 3.)_

Shipped: `prunePassCooldown = 5 * time.Second` gate — `prunePassDue()`
checks the shared `lastPrunePass` atomic stamp and `runPrunePass()`
stamps before probing; `reserveClientSlotWithPrune` gates only the
probe (its slot-wait poll always runs) and the server wake path shares
the same stamp via `runPrunePass()`, so wake and accept-loop passes
cannot stampede each other. `preserveGlobals` resets/restores
cooldown + stamp so tests stay hermetic. Tests:
`TestPrunePassCooldownOnAcceptLoop`,
`TestPrunePassCooldownSharedWithWake`. CHANGELOG's "one pass per
joiner" claim corrected. Commit: `ade0dba`; Test + Lint + CodeQL
green in CI.

### 2026-09-25 — `--password-file` for `-s` secure mode

1. **`CLIPPORT_PASSWORD` env / `--password-file` for `-s`** — ~30 minutes
   - `-s` reads the shared password from an interactive prompt, so scripted secure deployments either fall back to
     plaintext or embed the password in launchd/systemd command lines. Reading it from an env var or file pairs with
     the headless plaintext opt-in for unattended secure mode too. _(Promoted to Top 3.)_

Shipped: `passwordFromSources(file)` reads `--password-file`
(trailing `\r\n` stripped; empty/missing file errors) with the env
half (`CLIPPORT_SECRET`/`CLIPPORT_PASSWORD`) existing already;
file + env together or both env names set → mutual-exclusion error;
`--password-file` rejected without `-s` or with `-k`. Flag var
`passwordFile`, help text + `README` updated. Tests:
`TestPasswordFromFile`, `TestPasswordEnvSources`. Commit: `1a81f27`;
Test + Lint + CodeQL green in CI.

### 2026-09-24 — Fuzz-failure artifact upload

1. **Fuzz-failure artifact upload** — ~20 minutes
   - When the CI fuzz job finds a crasher, Go writes it under `testdata/fuzz/…`, but the runner's workspace is
     discarded. Upload that directory as a workflow artifact on failure so the corpus can be committed and the bug
     reproduced locally. _(Promoted to Top 3.)_

Shipped: the fuzz job in `.github/workflows/test.yml` gained an
`actions/upload-artifact@v4` step gated on `failure()` — uploads
`testdata/fuzz/` as the `fuzz-crashers` artifact (14-day retention,
`if-no-files-found: ignore` so a failure before checkout doesn't error
the step). CHANGELOG's fuzz-job entry extended to describe it.
Commit: `252539c`; Test + Lint + CodeQL green in CI.

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
