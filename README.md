# Clipport - Universal Clipboard

Apple users, did you know you could copy from one device and paste on the other? Wouldn't it be awesome if you could do that for non-Apple devices too?

Now you can, Apple device or not!

You don't even have to sign in like you need to on Apple devices. You don't have to install Go either!

*Clipport is a fork of [quackduck/uniclip](https://github.com/quackduck/uniclip) with a pinnable listen port (`-p`/`--port`), automatic reconnect, and an optional per-device keypair (`-k`/`--key`) as an alternative to a shared password.*

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

Usage: clipport [--port/-p] [--secure/-s] [--key/-k] [--debug/-d] [ <address> | --help/-h ]
       clipport keygen
Examples:
   clipport                                   # start a new clipboard with randomized port
   clipport -p 6666                           # start a new clipboard on a set port number
   clipport -d                                # start a new clipboard with debug output
   clipport 192.168.86.24:53701               # join the clipboard at 192.168.86.24:53701
   clipport 192.168.86.24 -p 53701            # same as above, host and port given separately
   clipport -d --secure 192.168.86.24:53701   # join the clipboard with debug output and enable encryption
   clipport keygen                            # generate a clipport keypair for use with --key
   clipport -k 192.168.86.24:53701            # join using keypair-based encryption instead of a password
Running just `clipport` will start a new clipboard.
It will also provide an address with which you can connect to the same clipboard with another device.
```

*Note: The devices have to be on the same local network (eg. connected to the same Wi-Fi) unless the device has a public IP with all ports routed to it. (use the public IP instead of what Clipport prints in this case)*

## Encryption

By default, clipport asks for confirmation before sending your clipboard in plaintext. Two ways to encrypt instead:

- **Shared password** (`-s`/`--secure`): prompts for a password, or reads one from the
  `CLIPPORT_SECRET` environment variable if set (set it on both devices to skip the prompt on
  both ends).
- **Per-device keypair** (`-k`/`--key`): run `clipport keygen` once per device, then use `-k`
  instead of `-s`. No secret ever has to be typed or shared — devices exchange public keys and
  derive a shared secret automatically. The first connection to a given peer trusts its public
  key and remembers it under `~/.clipport/known_peers`; if that peer's key ever changes later,
  clipport aborts the connection with a warning instead of silently proceeding.

Use one or the other, not both.

## Installing

Clipport isn't published to any package managers yet — build it from source with Go:

```sh
git clone https://github.com/tsyche/clipport.git
cd clipport
go build -o clipport .
```

Then move the `clipport` binary onto your `PATH` (e.g. `/usr/local/bin` on macOS/Linux).

### Runtime dependencies

- **GNU/Linux**: at least one of `xsel`, `xclip`, or `wl-clipboard` (Wayland) is needed
- **Android (Termux)**: install the Termux:API app from the Play Store, then run `pkg install termux-api`

## Uninstalling

Delete the `clipport` binary from wherever you placed it.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for setup, workflow, and branching conventions. See [CHANGELOG.md](CHANGELOG.md) for notable changes.

## Any other business

Have a question, idea or just want to share something? Head over to [Issues](https://github.com/tsyche/clipport/issues)

Thanks to [@aaryanporwal](https://github.com/aaryanporwal) for the original idea, and to the [quackduck/uniclip](https://github.com/quackduck/uniclip) contributors!
