# Clipport - Universal Clipboard

[![Test](https://github.com/tsyche/clipport/actions/workflows/test.yml/badge.svg)](https://github.com/tsyche/clipport/actions/workflows/test.yml)
[![License: MIT](https://img.shields.io/github/license/tsyche/clipport)](LICENSE)

_Clipport is a fork of [quackduck/uniclip](https://github.com/quackduck/uniclip) with a pinnable listen port (`-p`/`--port`), automatic reconnect, and an optional per-device keypair (`-k`/`--key`) as an alternative to a shared password._

## Usage

Run this to start a new clipboard:

```sh
clipport
```

Example output:

```text
Warning: no encryption requested (-s or -k). Clipboard contents will be sent in plaintext. Continue? [y/N] y
Starting a new clipboard
Run `clipport 192.168.86.24:51607` to join this clipboard

```

(Running without `-s` or `-k` always asks for this confirmation first — see [Encryption](#encryption) below to skip it.)

Just enter what it says (`clipport 192.168.86.24:51607`) on your other device with Clipport installed and hit enter. That's it! Now you can copy from one device and paste on the other.

You can even have multiple devices joined to the same clipboard (just run that same command on the new device).

```text
Clipport - Universal Clipboard
With Clipport, you can copy from one device and paste on another.

Usage: clipport [--port/-p] [--secure/-s] [--key/-k] [--debug/-d] [--quiet/-q] [--max-clients N] [ <address> | --help/-h ]
       clipport keygen
       clipport known-hosts [list|remove <peer>]
       clipport status
Examples:
   clipport                                   # start a new clipboard with randomized port
   clipport -p 6666                           # start a new clipboard on a set port number
   clipport -d                                # start a new clipboard with debug output
   clipport 192.168.86.24:53701               # join the clipboard at 192.168.86.24:53701
   clipport 192.168.86.24 -p 53701            # same as above, host and port given separately
   clipport [fe80::1]:53701                   # join via IPv6 (bracketed form; bare ::1 -p 53701 also works)
   clipport -d --secure 192.168.86.24:53701   # join the clipboard with debug output and enable encryption
    clipport keygen                            # generate a clipport keypair for use with --key
    clipport -k 192.168.86.24:53701            # join using keypair-based encryption instead of a password
    clipport known-hosts                       # list trusted -k peers
    clipport known-hosts remove 192.168.86.24  # forget a peer (after key rotation; peer IDs are host-only)
    clipport status                           # list connected clients of the running server
Running just `clipport` will start a new clipboard.
It will also provide an address with which you can connect to the same clipboard with another device.
```

State (keys, `known_peers`) lives in `~/.clipport` by default; set `CLIPPORT_DIR` or pass `--dir` to use another directory (containers, CI, multiple profiles). Pass `--quiet`/`-q` to suppress status lines when running headless (errors and prompts still print). The server accepts at most `--max-clients` peers (default 8, `0` = unlimited); extras are rejected.

> **Note:** The devices have to be on the same local network (eg. connected to the same Wi-Fi) unless the device has a public IP with all ports routed to it. (use the public IP instead of what Clipport prints in this case)

## Encryption

By default, clipport asks for confirmation before sending your clipboard in plaintext. Two ways to encrypt instead:

- **Shared password** (`-s`/`--secure`): prompts for a password, or reads one from the
  `CLIPPORT_SECRET` environment variable if set (set it on both devices to skip the prompt on
  both ends).
- **Per-device keypair** (`-k`/`--key`): run `clipport keygen` once per device, then use `-k`
  instead of `-s`. No secret ever has to be typed or shared — devices exchange public keys and
  derive a shared secret automatically. The first connection to a given peer trusts its public
  key and remembers it under `~/.clipport/known_peers`; if that peer's key ever changes later,
  clipport aborts the connection with a warning instead of silently proceeding. Manage trusted
  peers with `clipport known-hosts` (list) and `clipport known-hosts remove <peer>` (forget).

Use one or the other, not both.

Secure connections (`-k` or `-s`) reconnect automatically if the link drops (with exponential
backoff, 3s up to 30s). A permanent `-k` key mismatch does not retry — fix it with
`clipport known-hosts remove <peer>` and reconnect. Plaintext connections exit on drop instead
of reconnecting, to avoid silently re-admitting an unverifiable peer.

The server exits automatically once every connected device has disconnected (or on Ctrl+C, which also tells connected clients to exit instead of trying to reconnect).

## Security model

Clipport is built for a trusted local network (your Wi-Fi). The TCP port has no
access control of its own — what each mode protects against:

- **Plaintext (no `-s`/`-k`)** — no confidentiality, no integrity, no peer
  identity. Anyone who can reach the port can join the clipboard and read or
  inject whatever is copied, and nothing distinguishes a real peer from an
  impostor. The startup confirmation exists to stop _accidental_ exposure, not
  attackers. Plaintext links never auto-reconnect, so a dropped session cannot
  silently be replaced by another host. `--max-clients` can bound how many
  peers attach, but that is churn control, not authentication.
- **Shared password (`-s`/`--secure`)** — clipboard frames are encrypted with
  AES-256-GCM using a key derived from the password via scrypt. Passive
  eavesdroppers on the network see ciphertext; without the password they also
  cannot inject frames. Everyone who knows the password is equally trusted —
  there is no per-device identity, so treat the password like a room key.
- **Per-device keypair (`-k`/`--key`)** — the same AES-256-GCM encryption, but
  keys are derived from an X25519 exchange and each device's public key is
  pinned under `~/.clipport/known_peers` on first connection (trust on first
  use). After that, a changed key aborts the session instead of silently
  proceeding, which blocks both eavesdropping/injection and peer
  impersonation. First contact still trusts whatever key answers — the same
  caveat as first-time SSH — so verify fingerprints out of band if your threat
  model includes an active attacker from the very first connect.

No mode protects against a compromised device: anything with your user
account can read the OS clipboard directly. Don't expose the listen port to
the internet; use `-k` even on LANs you don't fully control.

## Installing

### macOS

```sh
brew install tsyche/tap/clipport
```

Or grab a binary from the [releases page](https://github.com/tsyche/clipport/releases) and move it to `/usr/local/bin/clipport`.

### Linux

```sh
brew install tsyche/tap/clipport
```

Or grab a binary from the [releases page](https://github.com/tsyche/clipport/releases) and move it to `/usr/local/bin/clipport`.

> **Note:** at least one of `xsel`, `xclip`, or `wl-clipboard` is required (on Wayland, `wl-clipboard` is preferred when present).

### NixOS / Nix

```sh
nix run github:tsyche/clipport
```

Or to install permanently:

```sh
nix profile install github:tsyche/clipport
```

### Windows

Grab a binary from the [releases page](https://github.com/tsyche/clipport/releases) and place it somewhere on your `PATH`.

### Android (Termux)

1. Install [Termux](https://termux.dev) and the [Termux:API](https://play.google.com/store/apps/details?id=com.termux.api) app
2. Run `pkg install termux-api` inside Termux
3. Grab the `linux_arm64` binary from the [releases page](https://github.com/tsyche/clipport/releases) and move it to `$PREFIX/usr/bin/clipport`

### Build from source

Requires Go:

```sh
git clone https://github.com/tsyche/clipport.git
cd clipport
go build -o clipport .
```

## Uninstalling

| Method   | Command                                                  |
| -------- | -------------------------------------------------------- |
| Homebrew | `brew uninstall clipport`                                |
| Nix      | `nix profile remove clipport`                            |
| Manual   | Delete the `clipport` binary from wherever you placed it |
| Termux   | Delete `$PREFIX/usr/bin/clipport`                        |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, workflow, and branching conventions. See [CHANGELOG.md](CHANGELOG.md) for notable changes.

## Any other business

Have a question, idea or just want to share something? Head over to [Issues](https://github.com/tsyche/clipport/issues)

Thanks to [@aaryanporwal](https://github.com/aaryanporwal) for the original idea, and to the [quackduck/uniclip](https://github.com/quackduck/uniclip) contributors!
