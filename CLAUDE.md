# Clipport

Cross-platform shared clipboard over TCP. Copy on one device, paste on another — no account required. Fork of [quackduck/uniclip](https://github.com/quackduck/uniclip).

## Stack

- **Language**: Go (single binary, no CGO)
- **Clipboard**: platform-native (`pbpaste`/`pbcopy` on macOS, `clip`/powershell on Windows, `xclip`/`xsel`/`wl-paste` on Linux)
- **Encryption**: AES-256-GCM via scrypt key derivation (`--secure` mode)
- **Release**: goreleaser cross-compiles for darwin/linux/windows × amd64/arm/arm64 (brew tap config still points at upstream quackduck's tap — not yet set up for this fork)

## Key commands

```sh
just build    # compile binary
just test     # go test -race ./...
just lint     # go vet ./...
just clean    # remove binary
```

## Key files

- `clipport.go` — all application logic (single file)
- `clipport_test.go` — crypto unit tests
- `.goreleaser.yml` — release config (cross-compile + brew tap)
- `flake.nix` — Nix build

## Architecture

Single-file Go app. One device runs as server (`makeServer`), others connect as clients (`ConnectToServer`). The server broadcasts clipboard changes to all connected clients. Encryption is opt-in via `--secure` with a shared password.

## Branch notes

- `add-custom-ports` — adds `-p`/`--port` flag to pin listen port instead of randomizing
- Fork remote: `https://github.com/tsyche/clipport`
- Upstream: `https://github.com/quackduck/uniclip`
- Renamed from `uniclip` to `clipport` to give the fork its own identity
