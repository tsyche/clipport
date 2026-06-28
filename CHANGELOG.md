# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.1.1] - 2026-06-28

### Added
- Homebrew tap (`brew tap tsyche/tap && brew install clipport`)
- `just install` recipe to build and install binary to `/usr/local/bin`
- Pre-release test step in GitHub Actions release workflow

### Changed
- Plaintext connections now exit on drop instead of reconnecting, with a message
  suggesting `-k` or `-s`; secure connections (`-k`/`-s`) still reconnect automatically
- Server distinguishes secure vs plaintext peers on drop: secure peers get a
  re-verification notice, plaintext peers get a warning they cannot be re-admitted

## [0.1.0] - 2026-06-28

### Added
- `-k`/`--key` keypair encryption mode: `clipport keygen` generates a per-device X25519
  key, connections derive a shared secret via ECDH, and peers are trusted-on-first-connect
  (`~/.clipport/known_peers`)
- `CLIPPORT_SECRET` environment variable to supply the `--secure` password without a prompt
- `host -p port` as an alternative to `host:port` for the client address
- Confirmation prompt before connecting without `-s` or `-k` (plaintext warning)
- `-p`/`--port` flag to pin the listen port instead of randomizing
- GitHub Actions CI (tests, lint, CodeQL) and goreleaser release workflow

### Changed
- Client auto-reconnects on a dropped connection (secure mode) instead of exiting
- TCP keepalive enabled on all connections to detect NAT/firewall idle timeouts

### Fixed
- Client never actually used a password in `--secure` mode
