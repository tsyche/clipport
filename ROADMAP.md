# Roadmap

Inferred from the codebase on 2026-06-16 (no prior ROADMAP.md existed). Single-file Go app — see `clipport.go`, `AGENTS.md`/`CLAUDE.md` for architecture.

## Recently Completed

1. **Custom listen port** — `-p`/`--port` flag to pin the server port instead of randomizing
2. **Concurrency safety** — shared globals (`listOfClients`, `localClipboard`) guarded by a mutex
3. **Crash fix** — `decrypt()` no longer panics on truncated/malformed ciphertext, returns an error instead
4. **Crypto test coverage** — `clipport_test.go` covers encrypt/decrypt roundtrip, wrong-key failure, truncated input, deterministic key derivation
5. **Project rebrand** — forked and renamed from `quackduck/uniclip` to its own identity (`tsyche/clipport`)

## Top 3 Suggested Tasks

1. **Set up this fork's own distribution** — ~2-4 hours
   - `.goreleaser.yml` still points at the upstream `quackduck` Homebrew tap and homepage; this fork has no published releases
   - Deliberately deferred by design (per project notes) — pick this up when ready to cut a real release
2. **Root-cause the empty-clipboard workaround** — ~1-2 hours
   - `clipport.go:204` has a `// hacky way to prevent empty clipboard TODO: find out why empty cb happens` — currently just silently drops empty payloads instead of fixing the source
3. **Harden the wire protocol against oversized/malformed frames** — ~2-3 hours
   - `gob.NewDecoder(r).Decode(...)` in `MonitorSentClips` has no message-size cap — a malicious or buggy peer could send an unbounded payload; add a `io.LimitReader` or explicit max-size check before decoding

## Inherited from upstream (quackduck/uniclip) — triaged 2026-06-16

Checked the upstream repo's open issues against this fork's actual code (not just assumed carried over):

1. **Windows clipboard CRLF corruption** — `runGetClipCommand()` (clipport.go:337-338) only trims a *trailing* `\r\n` from PowerShell's `Get-Clipboard` output; internal `\r\n` line endings in multi-line text are left untouched. Upstream [quackduck/uniclip#35](https://github.com/quackduck/uniclip/issues/35) reports a client receiving a second, `\r\n`-corrupted copy of multi-line content. Upstream has an unmerged fix ([PR #36](https://github.com/quackduck/uniclip/pull/36)) doing `strings.ReplaceAll(str, "\r\n", "\n")` before trimming — straightforward to port. ~30 min.
2. **Wayland + xclip picks the wrong backend** — `runGetClipCommand`/`setLocalClip` (clipport.go:320-325, 355-360) check `xclip` before `wl-paste`/`wl-copy`. On a Wayland session that happens to have `xclip` installed (common — some distros ship it for X11-compat apps), clipport silently fails with `exit status 1` instead of using the Wayland-native tool. Upstream: [quackduck/uniclip#26](https://github.com/quackduck/uniclip/issues/26). Fix: check `$WAYLAND_DISPLAY` first and prefer `wl-paste`/`wl-copy` when set. ~1 hr.
3. **Endless error spam on non-text clipboard content** — when the system clipboard holds something `pbpaste`/`xclip`/etc. can't read as text (e.g. an image), `runGetClipCommand` (clipport.go:333-336) calls `handleError` and returns a sentinel string every poll cycle, forever, with no backoff or one-time warning. Upstream: [quackduck/uniclip#23](https://github.com/quackduck/uniclip/issues/23). Fix: rate-limit/dedupe the error or warn once and skip until clipboard content type changes. ~1-2 hrs.

Lower priority / not clearly actionable yet:
- **"use of closed network connection" after Windows hibernation** ([quackduck/uniclip#32](https://github.com/quackduck/uniclip/issues/32)) — reporter couldn't reliably reproduce; revisit if it recurs for us.
- Custom-port feature request ([quackduck/uniclip#20](https://github.com/quackduck/uniclip/issues/20)) is already done in this fork via `-p`/`--port`.

## Backlog

- **Transport security for non-`--secure` mode** — cleartext mode has no authentication between peers; anyone who can reach the port can join the clipboard. Worth at least documenting as a known limitation if not fixing.
- **Reconnect on dropped connection** — clients currently just exit (`return` on EOF) rather than retrying; a flaky network kills the session permanently.
- **`flake.nix` `vendorSha256` staleness check** — unverified against current `go.mod`/`go.sum` since the rebrand; likely fine but not confirmed.

## Notes

- This roadmap was bootstrapped from `TODO` comments, recent git history, and reading `clipport.go` directly — there was no prior roadmap or stated long-term vision to preserve. Revisit priorities once real users/usage patterns emerge.
