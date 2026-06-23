# Roadmap

Inferred from the codebase on 2026-06-16 (no prior ROADMAP.md existed); audited and updated 2026-06-23. Single-file Go app — see `clipport.go`, `AGENTS.md`/`CLAUDE.md` for architecture.

## Recently Completed

1. **Reconnect + TCP keepalive** — client auto-reconnects on a dropped connection instead of exiting; keepalive enabled on every connection to catch NAT/firewall idle timeouts (this was a standing backlog item, now done)
2. **Per-device keypair encryption** — `-k`/`--key` mode: `clipport keygen` generates an X25519 keypair, connections derive a shared secret via ECDH, peers trusted-on-first-connect (`~/.clipport/known_peers`) with a loud abort on key mismatch
3. **Plaintext confirmation gate** — connecting without `-s` or `-k` now warns and requires confirmation, partially addressing the transport-security backlog item below
4. **`CLIPPORT_SECRET` env var + flexible client addressing** — skips the `--secure` password prompt when set; client accepts `host -p port` as an alternative to `host:port`
5. **Custom listen port** — `-p`/`--port` flag to pin the server port instead of randomizing

## Top 3 Suggested Tasks

1. **Add a CI workflow that runs `go test -race ./...`** — ~1 hour
   - Verified 2026-06-23: neither `codeql-analysis.yml` nor `linter.yml` runs the test suite — only a security scan and a markdown/secrets linter. The suite (and `just test`) only runs if someone remembers to run it locally. Highest impact-to-effort ratio of anything on this roadmap right now, especially with new untested networking/crypto code landing (see Backlog).
2. **Harden the wire protocol against oversized/malformed frames** — ~2-3 hours
   - `gob.NewDecoder(r).Decode(...)` in `MonitorSentClips` (clipport.go:497) has no message-size cap — a malicious or buggy peer could send an unbounded payload; add a `io.LimitReader` or explicit max-size check before decoding
3. **Root-cause the empty-clipboard workaround** — ~1-2 hours
   - `clipport.go:516` has a `// hacky way to prevent empty clipboard TODO: find out why empty cb happens` — currently just silently drops empty payloads instead of fixing the source

## Inherited from upstream (quackduck/uniclip) — triaged 2026-06-16

Checked the upstream repository's open issues against this fork's actual code (not just assumed carried over):

1. **Windows clipboard CRLF corruption**
   `runGetClipCommand()` (clipport.go:649-650) only trims a *trailing* `\r\n` from PowerShell's `Get-Clipboard` output; internal `\r\n` line endings in multi-line text are left untouched.
   Upstream [quackduck/uniclip#35](https://github.com/quackduck/uniclip/issues/35) reports a client receiving a second, `\r\n`-corrupted copy of multi-line content.
   Upstream has an unmerged fix ([PR #36](https://github.com/quackduck/uniclip/pull/36)) doing `strings.ReplaceAll(str, "\r\n", "\n")` before trimming — straightforward to port. ~30 min.
2. **Wayland + xclip picks the wrong backend**
   `runGetClipCommand`/`setLocalClip` (clipport.go:632-637, 667-672) check `xclip` before `wl-paste`/`wl-copy`.
   On a Wayland session that happens to have `xclip` installed (common — some distros ship it for X11-compat apps), clipport silently fails with `exit status 1` instead of using the Wayland-native tool.
   Upstream: [quackduck/uniclip#26](https://github.com/quackduck/uniclip/issues/26). Fix: check `$WAYLAND_DISPLAY` first and prefer `wl-paste`/`wl-copy` when set. ~1 hr.
3. **Endless error spam on non-text clipboard content**
   When the system clipboard holds something `pbpaste`/`xclip`/etc. can't read as text (e.g. an image), `runGetClipCommand` (clipport.go:645-647) calls `handleError` and returns a sentinel string every poll cycle, forever, with no backoff or one-time warning.
   Upstream: [quackduck/uniclip#23](https://github.com/quackduck/uniclip/issues/23). Fix: rate-limit/dedupe the error or warn once and skip until clipboard content type changes. ~1-2 hrs.

Lower priority / not clearly actionable yet:
- **"use of closed network connection" after Windows hibernation** ([quackduck/uniclip#32](https://github.com/quackduck/uniclip/issues/32)) — reporter couldn't reliably reproduce; revisit if it recurs for us.
- Custom-port feature request ([quackduck/uniclip#20](https://github.com/quackduck/uniclip/issues/20)) is already done in this fork via `-p`/`--port`.

## New Suggestions (2026-06-23)

1. **`clipport known-hosts` management subcommand** — ~1-2 hours
   - Right now the only way to remove a stale/rotated peer from `~/.clipport/known_peers` is to hand-edit the file (the mismatch warning even says so). A small `list`/`remove <peer>` subcommand would make key rotation usable without asking users to edit a text file by hand — same idea as `ssh-keygen -R`.
2. **Exponential backoff on client reconnect** — ~30-60 minutes
   - `ConnectToServer`'s retry loop currently retries every fixed 3 seconds forever. Fine for a brief network blip, but if the server is down for an extended period (or a `-k` peer mismatch is permanent — see clipport.go's `resolveConnectionKey`) this spams reconnect attempts and log output indefinitely. Cap with backoff (e.g. 3s → 30s) instead.
3. **Distinguish permanent vs. transient reconnect failures** — ~1 hour
   - Related to the above: a `-k` peer-mismatch rejection is permanent (will never succeed by retrying) but currently gets the same "Reconnecting..." treatment as a transient network drop. Worth a distinct code path that stops retrying and tells the user to fix the key mismatch instead of looping.

## Backlog

- **Set up this fork's own distribution** — ~2-4 hours
  - `.goreleaser.yml` still points at the upstream `quackduck` Homebrew tap and homepage; this fork has no published releases. Deliberately deferred by design (per project notes) — pick this up when ready to cut a real release.
- **Transport security for non-encrypted mode** — cleartext mode still has no authentication between peers; anyone who can reach the port can join the clipboard. The plaintext confirmation gate (added 2026-06-23) at least makes this an explicit, opt-in choice rather than a silent default — but the underlying gap (no auth) is unchanged.
- **Test coverage for new networking/crypto code** — the 2026-06-23 changes (ECDH handshake,
  TOFU trust store, reconnect loop, keygen, CLI flag combining) shipped with no new tests;
  `clipport_test.go` still only covers the original encrypt/decrypt/deriveKey helpers, and the
  wire protocol (`sendClipboard`/`MonitorSentClips`/`MonitorLocalClip`) has had 0% coverage
  since before this session. Deliberately deferred for now — revisit with a fresh `/audit-tests`
  run once ready. ~4-6 hours for happy-path coverage of both the new code and the pre-existing gap.
- **`flake.nix` `vendorSha256` staleness check** — unverified against current `go.mod`/`go.sum` since the rebrand; likely fine but not confirmed.

## Notes

- This roadmap was bootstrapped from `TODO` comments, recent git history, and reading `clipport.go` directly — there was no prior roadmap or stated long-term vision to preserve. Revisit priorities once real users/usage patterns emerge.
