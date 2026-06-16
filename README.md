# Clipport - Universal Clipboard

Apple users, did you know you could copy from one device and paste on the other? Wouldn't it be awesome if you could do that for non-Apple devices too?

Now you can, Apple device or not!

You don't even have to sign in like you need to on Apple devices. You don't have to install Go either!

*Clipport is a fork of [quackduck/uniclip](https://github.com/quackduck/uniclip) with support for pinning a specific listen port via `-p`/`--port`.*

## Usage

Run this to start a new clipboard:

 ```sh
clipport
```

Example output:

```text
Starting a new clipboard!
Run `clipport 192.168.86.24:51607` to join this clipboard

```

Just enter what it says (`clipport 192.168.86.24:51607`) on your other device with Clipport installed and hit enter. That's it! Now you can copy from one device and paste on the other.

You can even have multiple devices joined to the same clipboard (just run that same command on the new device).

```text
Clipport - Universal Clipboard
With Clipport, you can copy from one device and paste on another.

Usage: clipport [--port/-p] [--secure/-s] [--debug/-d] [ <address> | --help/-h ]
Examples:
   clipport                                   # start a new clipboard with randomized port
   clipport -p 6666                           # start a new clipboard on a set port number
   clipport -d                                # start a new clipboard with debug output
   clipport 192.168.86.24:53701               # join the clipboard at 192.168.86.24:53701
   clipport -d --secure 192.168.86.24:53701   # join the clipboard with debug output and enable encryption
Running just `clipport` will start a new clipboard.
It will also provide an address with which you can connect to the same clipboard with another device.
```

*Note: The devices have to be on the same local network (eg. connected to the same Wi-Fi) unless the device has a public IP with all ports routed to it. (use the public IP instead of what Clipport prints in this case)*

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
