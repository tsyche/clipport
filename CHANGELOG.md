# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added
- `-k`/`--key` keypair encryption mode: `clipport keygen` generates a per-device X25519 key, connections derive a shared secret via ECDH, and peers are trusted-on-first-connect (`~/.clipport/known_peers`)
- `CLIPPORT_SECRET` environment variable to supply the `--secure` password without a prompt
- `host -p port` as an alternative to `host:port` for the client address
- Confirmation prompt before connecting without `-s` or `-k` (plaintext warning)

### Changed
- Client now auto-reconnects on a dropped connection instead of exiting
- TCP keepalive enabled on all connections to detect NAT/firewall idle timeouts

### Fixed
- Client never actually used a password in `--secure` mode
