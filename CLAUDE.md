# Clipport

Cross-platform shared clipboard over TCP. Copy on one device, paste on another — no account required. Fork of [quackduck/uniclip](https://github.com/quackduck/uniclip).

## Stack

- **Language**: Go (single binary, no CGO)
- **Clipboard**: platform-native (`pbpaste`/`pbcopy` on macOS, `clip`/PowerShell on Windows, `xclip`/`xsel`/`wl-paste` on Linux — Wayland sessions with `$WAYLAND_DISPLAY` set prefer `wl-paste`/`wl-copy`); PNG/JPEG images sync too (magic-sniffed frames, platform image read/write)
- **Encryption**: AES-256-GCM, keyed either via scrypt over a shared password (`--secure`/`-s`) or an ECDH-derived secret from a per-device X25519 keypair (`--key`/`-k`, generated with `clipport keygen`)
- **Release**: goreleaser cross-compiles for darwin/linux/windows/FreeBSD × amd64/arm/arm64/386 (excluding windows/arm64); push a `v*` tag to trigger the GitHub Actions release workflow, publish binaries to GitHub Releases, and push a formula to `tsyche/homebrew-tap`

## Key commands

```sh
just setup       # go mod download
just build       # compile binary
just test        # go test -race ./...
just lint        # go vet ./...
just lintfix     # gofmt -w .
just clean       # remove binary
just fresh       # clean + build
just install     # build and install to /usr/local/bin
just sync-docs   # copy newer of CLAUDE.md/AGENTS.md over the other
```

## Key files

- `clipport.go` — all application logic (single file)
- `clipport_test.go` — unit tests (crypto, wire protocol, TOFU/handshake, monitors) + fuzz seed
- `.goreleaser.yml` — release config (cross-compile + brew tap)
- `flake.nix` — Nix build
- `CONTRIBUTING.md` — setup, workflow, and branching conventions for contributors

## Architecture

Single-file Go app. One device runs as server (`makeServer`), others connect as clients
(`ConnectToServer`); secure connections (`-k`/`-s`) reconnect automatically (with TCP
keepalive on every connection) if the link drops; clients also detect local suspend/resume
via a wall-clock poll gap and redial immediately (skipping backoff); plaintext connections
exit on drop. The server broadcasts clipboard changes to all connected
clients, and exits once every connected client has disconnected and none returns within a
10s grace period (`HandleClient` → `exitIfStillEmptyAfter`). Encryption is opt-in: `--secure`/`-s` for a shared password, or `--key`/`-k` for a
per-device keypair with trust-on-first-connect peer verification. Without either, clipport
prompts for confirmation before sending the clipboard in plaintext. On Ctrl+C, the server propagates shutdown to connected clients via TCP FIN so they exit cleanly instead of trying to reconnect.

## Fork notes

- Fork remote: `https://github.com/tsyche/clipport`
- Upstream: `https://github.com/quackduck/uniclip`
- Renamed from `uniclip` to `clipport`, and the `-p`/`--port` flag (pins the listen port instead of randomizing) added, then merged into `main`
- Default branch renamed `master` → `main`
