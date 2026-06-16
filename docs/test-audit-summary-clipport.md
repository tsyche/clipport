# Clipport test audit summary

Last updated: 2026-06-16
Mode: lite | Run `--deep` for full deep-dive

## Summary

**Overall health: poor.** 7 tests, all in `clipport_test.go`, all exercising only the crypto helpers (`encrypt`/`decrypt`/`deriveKey`) — 13.5% statement coverage measured live. The other 13 functions in `clipport.go`, including the actual clipboard-sync wire protocol, have zero tests. CI is currently misconfigured to not gate PRs at all (stale `master` branch references), and a markdownlint failure already happened once and will recur on the next push touching `ROADMAP.md`.

### Suite health scorecard

| Dimension | Score | Notes |
|-----------|-------|-------|
| Feature coverage | 🔴 15% | 1/6 functional areas covered (crypto only); wire-protocol, networking, clipboard-io, CLI parsing, error handling all 0% |
| Unit test coverage | 🔴 35% | 13.5% measured via `go test -cover` (live run, 2026-06-16) |
| Negative-path coverage | 🟡 60% | 2/7 tests (28.6%) — wrong-key decrypt, truncated ciphertext |
| CI integration | 🔴 15% | No workflow runs `go test` at all; CodeQL/linter PR triggers reference the now-deleted `master` branch |
| Assertion quality | — | `--deep` |
| Failure diagnostics | — | `--deep` |
| Test data isolation | — | `--deep` |
| Edge case balance | — | `--deep` |
| Test naming quality | — | `--deep` |
| Mock boundary clarity | — | `--deep` |
| Coverage regression risk | — | `--deep` |
| **Overall** | **🔴 30%** | **mean of 4 assessed dimensions, rounded to nearest 5** |

| Stat | Value |
|------|-------|
| Tests | 7 across 1 file |
| Coverage breadth | 1/6 areas (17%) |
| Skipped/xfailed | 0 |
| Open P0/P1 items | 4 |

**Top priority items:**

| Priority | Item | Notes | Issue |
|----------|------|-------|-------|
| P0 | CI workflows reference the deleted `master` branch | `codeql-analysis.yml` (push+PR triggers) and `linter.yml` (PR trigger, `DEFAULT_BRANCH`) all say `branches: [master]` — `master` was renamed to `main` and deleted from origin earlier today, so no PR-gated CI runs against `main`-targeted PRs until these are updated | — |
| P0 | `ROADMAP.md` will fail the markdownlint CI step on its next push | Lines (currently 26-28, 501-577 chars) exceed `MD013`'s 400-char limit — this already failed once in CI (run `27631105894`, commit `9e2470117`) and was never fixed; it just didn't resurface because the next push didn't touch the file | — |
| P1 | Core wire-protocol functions have 0% test coverage | `sendClipboard`, `MonitorSentClips`, `MonitorLocalClip` operate on `bufio.Writer`/`Reader` — testable with a `bytes.Buffer`/`io.Pipe`, no real network needed — yet none are tested. This is the actual clipboard-sync logic, the project's core value | — |
| P1 | No CI workflow runs the test suite | Only CodeQL (security scan) and a markdown/secrets linter exist; `go test`/`just test` has never run in CI — the 7 existing tests only catch regressions if someone remembers to run them locally | — |

## Scope

- Audited: `clipport.go` (415 lines, single `package main`) + `clipport_test.go` (115 lines) at repo root — tests are colocated with source, not an aggregated test repo.
- Framework: Go's built-in `testing` package, run via `go test` (`just test` → `go test -race ./...`).
- Environment: none required — all 7 tests are pure unit tests with no external services, files, or network.
- Excluded: nothing in scope was excluded; no test files found outside `clipport_test.go`.

## Quick metrics

| Metric | Value |
|--------|-------|
| Total test files | 1 |
| Total tests | 7 |
| Skipped/xfailed | 0 |
| Negative-path tests | 2 (28.6%) |
| Unit test files/cases | 1 file / 7 cases |
| Coverage tool | `go test -cover` (ad hoc; not wired into CI) |
| Hardcoded waits | 0/1 test files (0%) |

## Suite inventory

| File | Tests | Category |
|------|-------|----------|
| `clipport_test.go` | 7 | unit (crypto: encrypt/decrypt/deriveKey) |

## Unit test assessment

Measured live: `go test -coverprofile=... ./...` → **13.5% statement coverage**. Per-function breakdown (`go tool cover -func`):

| Function | Coverage |
|----------|----------|
| `decrypt` | 82.4% |
| `deriveKey` | 75.0% |
| `encrypt` | 73.3% |
| `main`, `makeServer`, `HandleClient`, `ConnectToServer`, `MonitorLocalClip`, `MonitorSentClips`, `sendClipboard`, `runGetClipCommand`, `getLocalClip`, `setLocalClip`, `getOutboundIP`, `handleError`, `debug` | 0.0% (13 functions) |

The 3 crypto functions are reasonably well covered (roundtrip, wrong-key failure, truncated-input, deterministic derivation, nil-salt generation, empty-plaintext). Everything else in the file — networking, the wire protocol, clipboard I/O, and CLI parsing — has never been exercised by a test.

## Coverage matrix

| Area | Status | Notes |
|------|--------|-------|
| Crypto (`encrypt`/`decrypt`/`deriveKey`) | ✅ | 7 tests: roundtrip, unique-nonce, wrong-key, truncated-ciphertext, deterministic salt, nil-salt, empty-plaintext |
| Wire protocol (`sendClipboard`, `MonitorSentClips`, `MonitorLocalClip`) | ❌ | 0 tests. Testable today via `bytes.Buffer`/`io.Pipe` — no real socket needed |
| Server/client networking (`makeServer`, `HandleClient`, `ConnectToServer`, `getOutboundIP`) | ❌ | 0 tests. Port-pinning logic (`-p`/`--port`, this fork's signature feature) is entirely unverified |
| Clipboard I/O (`runGetClipCommand`, `getLocalClip`, `setLocalClip`) | ❌ | 0 tests. Platform-dependent shell-outs (`pbpaste`/`xclip`/etc.) — harder to unit test without `exec.Command` mocking, but the Windows CRLF-handling path (`ROADMAP.md`'s inherited-bug #1) is exactly the kind of thing a table-driven test on the trim/replace logic could catch |
| CLI/arg parsing (`main`, `helpMsg`, flag setup) | ❌ | 0 tests. No coverage of `-p`/`--port`/`--secure`/`--debug` flag parsing or the too-many-arguments error path |
| Error handling/logging (`handleError`, `debug`) | ❌ | 0 tests. Low value to test directly — thin wrappers |

No UI in this project — accessibility dimension omitted (non-UI CLI tool).

## Skip/xfail inventory

None. No `t.Skip`, `t.Skipf`, or conditional skips found in `clipport_test.go`.

## Checklist probe results

1. **CI triggers** — CodeQL: push+PR (both scoped to deleted `master`) + weekly cron. Linter: push (unscoped, fires on any branch) + PR (scoped to deleted `master`). No workflow runs `go test`. → feeds CI integration score and P0 #1.
2. **Failure artifacts** — N/A, CLI tool, no UI test runner/trace concept applies.
3. **Coverage wiring** — `go test -cover` works locally; not wired into any CI workflow, no gate, no badge.
4. **Untracked skips** — none found (no skips exist).
5. **Silent-green skips** — none found.
6. **Hardcoded waits** — 0 occurrences of `sleep(`/`time.Sleep(`/wait-timeout patterns in `clipport_test.go`.
7. **Regression risk** — `git log --since='30 days ago'` shows 2 commits touching `clipport.go`: the rebrand (string-only renames, no logic change) and "Code audit fixes, crypto tests, and workflow setup" (the one real logic change — `decrypt()` panic fix — shipped with matching tests in the same commit). Clean.
8. **Mocked criticals** — N/A, no payment/auth/external-service integrations in this project.

## Top recommendations

**P0:**
1. Update `.github/workflows/codeql-analysis.yml` and `.github/workflows/linter.yml` to reference `main` instead of `master` (push/pull_request `branches:` lists, and linter's `DEFAULT_BRANCH` env var) — otherwise no PR-gated CI runs against `main`.
2. Wrap or shorten the long lines in `ROADMAP.md` (currently lines 26-28) to stay under markdownlint's 400-char `MD013` limit — this already failed CI once (run `27631105894`) and will fail again on the next push touching this file.

**P1:**
1. Add tests for `sendClipboard`/`MonitorSentClips`/`MonitorLocalClip` using a `bytes.Buffer` or `io.Pipe` in place of a real socket — this is the core clipboard-sync logic and it's currently unverified by anything.
2. Add a CI workflow step (or extend an existing one) that runs `go test -race ./...` on push/PR — right now the test suite only runs if a human remembers to run `just test` locally.

**P2-P4:** Populated by `--deep`.

## Audit based on

Audit based on: `clipport.go`, `clipport_test.go`, `justfile`, both files in `.github/workflows/`, a live `go test -coverprofile` run, and `gh api`/`gh run list` queries against `tsyche/clipport`. Skipped: nothing — no credentials or external services were needed for this audit.
