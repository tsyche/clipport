# Clipport

Cross-platform shared clipboard over TCP. Copy on one device, paste on another — no account required. Fork of [quackduck/uniclip](https://github.com/quackduck/uniclip).

## Stack

- **Language**: Go (single binary, no CGO)
- **Clipboard**: platform-native (`pbpaste`/`pbcopy` on macOS, `clip`/powershell on Windows, `xclip`/`xsel`/`wl-paste` on Linux)
- **Encryption**: AES-256-GCM via scrypt key derivation (`--secure` mode)
- **Release**: goreleaser cross-compiles for darwin/linux/windows/freebsd × amd64/arm/arm64/386 (excluding windows/arm64; brew tap config still points at upstream quackduck's tap — not yet set up for this fork)

## Key commands

```sh
just build       # compile binary
just test        # go test -race ./...
just lint        # go vet ./...
just clean       # remove binary
just fresh       # clean + build
just sync-docs   # copy newer of CLAUDE.md/AGENTS.md over the other
```

## Key files

- `clipport.go` — all application logic (single file)
- `clipport_test.go` — crypto unit tests
- `.goreleaser.yml` — release config (cross-compile + brew tap)
- `flake.nix` — Nix build
- `CONTRIBUTING.md` — setup, workflow, and branching conventions for contributors

## Architecture

Single-file Go app. One device runs as server (`makeServer`), others connect as clients (`ConnectToServer`). The server broadcasts clipboard changes to all connected clients. Encryption is opt-in via `--secure` with a shared password.

## Fork notes

- Fork remote: `https://github.com/tsyche/clipport`
- Upstream: `https://github.com/quackduck/uniclip`
- Renamed from `uniclip` to `clipport`, and the `-p`/`--port` flag (pins the listen port instead of randomizing) added, then merged into `main`
- Default branch renamed `master` → `main`
