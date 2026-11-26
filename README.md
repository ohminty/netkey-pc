# NetKey for Windows

Join [NetKey](https://netkey.no) LAN parties with your emulators and virtual machines, right from a Windows PC.

NetKey puts old PCs from different homes on one network over the internet, the way LAN parties used to be: IPX,
NetBEUI and TCP/IP games find each other as if everyone sat in the same room. Parties are hosted on a
**NetKey box** (a Raspberry Pi running NetKey). This app *joins* parties: with an invite code from a host, or from
the list of public parties.

## What it does

- Joins a party through the NetKey cloud hub, over an encrypted WireGuard link, with the host's approval.
- Connects your emulators to the party's network:
  - **VMware Workstation, VirtualBox, DOSBox-X**: through the *NetKey Adapter*, a virtual network adapter
    (OpenVPN's signed TAP-Windows6 driver). Windows itself stays off the party: its protocols are turned off on
    the adapter.
  - **86Box and QEMU**: through 86Box's Local Switch, with nothing extra to install.
- Party chat, public parties, a built-in connection test, and setup help for each emulator.

## Install

Download `NetKey-<version>-setup.exe` from [Releases](../../releases) and run it. It installs the app, the NetKey
service and the NetKey Adapter, and asks for administrator rights once.

The installer isn't code-signed yet (NetKey is a free, non-profit project), so Windows SmartScreen shows
"Windows protected your PC". Choose **More info → Run anyway**.

Requirements: Windows 10 1809 or later, or Windows 11, x64.

## How it's built

```
service/     netkeyd, the Windows service (Go): hub client, WireGuard (wireguard-go with its own TCP/IP stack),
             VXLAN to the party host, the switch, the NetKey Adapter and 86Box Local Switch ports, the named pipe
             the app talks to, install/uninstall
app/         the app (C#, WinUI 3, Windows App SDK): talks to the service over \\.\pipe\netkey
installer/   Inno Setup script, wizard artwork and third-party notices
```

The service does the work and keeps a party running when the window is closed; the app is only the window and the
notification-area icon.

### Building the installer (on Windows)

You need Go 1.26 or later, the .NET 10 SDK and Inno Setup 6:

```
winget install GoLang.Go Microsoft.DotNet.SDK.10 JRSoftware.InnoSetup
```

Then, from this folder:

```
powershell -ExecutionPolicy Bypass -File build-windows.ps1
```

It downloads TAP-Windows6 9.27.0 from OpenVPN's GitHub release (checked against a fixed SHA-256), builds the
service and the app, and writes `stage\out\NetKey-<version>-setup.exe`.

### Tests

```
cd service
go test ./...
```

The service also builds and its tests run on macOS and Linux (without the adapter and the Windows service).

## License

Apache License 2.0, see [LICENSE](LICENSE) and [NOTICE](NOTICE). The name "NetKey" and the logo are not covered
by the license. Bundled third-party software and its licenses are listed in
[installer/THIRD-PARTY.txt](installer/THIRD-PARTY.txt).
