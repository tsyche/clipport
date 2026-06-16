# Uniclip

Cross-platform shared clipboard over TCP. Copy on one device, paste on another — no account required.

## Stack

- **Language**: Go (single binary, no CGO)
- **Clipboard**: platform-native (`pbpaste`/`pbcopy` on macOS, `clip`/powershell on Windows, `xclip`/`xsel`/`wl-paste` on Linux)
- **Encryption**: AES-256-GCM via scrypt key derivation (`--secure` mode)
- **Release**: goreleaser cross-compiles for darwin/linux/windows × amd64/arm/arm64

## Key commands

```sh
just build    # compile binary
just test     # go test -race ./...
just lint     # go vet ./...
just clean    # remove binary
```

## Key files

- `uniclip.go` — all application logic (single file)
- `uniclip_test.go` — crypto unit tests
- `.goreleaser.yml` — release config (cross-compile + brew tap)
- `flake.nix` — Nix build

## Architecture

Single-file Go app. One device runs as server (`makeServer`), others connect as clients (`ConnectToServer`). The server broadcasts clipboard changes to all connected clients. Encryption is opt-in via `--secure` with a shared password.

## Branch notes

- `add-custom-ports` — adds `-p`/`--port` flag to pin listen port instead of randomizing
- Fork remote: `https://github.com/tsyche/uniclip`
- Upstream: `https://github.com/quackduck/uniclip`
