# NetKey for Windows

**Play old LAN games over the internet, from your emulators.** Doom, Duke Nukem 3D, Warcraft II, Command & Conquer,
Quake, Age of Empires: run them in DOSBox, VMware, VirtualBox or 86Box on your Windows PC, and play against friends
in other homes as if you were all at the same LAN party. **No port forwarding, no DOS drivers, encrypted.**

![NetKey for Windows: join a party with an invite code](docs/screenshot-start.png)

## Why do I need this?

In the 90s, multiplayer meant a **LAN**: a few PCs in one room, connected with network cables. Games found each other
on that cable using languages like **IPX** (Doom, Warcraft II, Duke Nukem 3D and most DOS games) or **NetBEUI**
(Windows file sharing).

The internet doesn't speak IPX, and it doesn't let games "shout" to find each other. So when you play those games in
an emulator today, the multiplayer menu works, but it can only find PCs in your own home.

**NetKey fixes that.** It connects your emulator to a LAN party hosted somewhere else, through an encrypted link.
To the game, it looks exactly like a network cable to your friend's house. Nothing to configure in the game, no
router settings, no port forwarding.

```
  Your PC                                        The host's home
  ┌──────────────────────────┐                   ┌───────────────────────────────┐
  │ DOSBox-X, VMware, 86Box  │                   │ NetKey box (a Raspberry Pi)   │
  │            │             │  encrypted link   │            │                  │
  │   NetKey for Windows ────┼── over the ───────┼──── the party's network ──────┼── old PCs,
  │                          │    internet       │                               │   other players
  └──────────────────────────┘                   └───────────────────────────────┘
```

## What you need

- A **Windows 10 or 11** PC (64-bit).
- An **emulator** with your game in it. Pick by the kind of game:

  | Your game runs on | Use | Extra install needed |
  |---|---|---|
  | **DOS** (Doom, Duke Nukem 3D, Warcraft II, C&C, Descent…) | DOSBox, [DOSBox-X](https://dosbox-x.com) or [DOSBox Staging](https://www.dosbox-staging.org) | nothing |
  | **Windows 95, 98 or XP** (Age of Empires, StarCraft, Diablo, Red Alert 2…) | VMware Workstation or VirtualBox, with Windows inside | nothing |
  | **A whole retro PC** (any of the above) | [86Box](https://86box.net) | nothing |

- **A party to join.** Either a friend who hosts one on a **NetKey box** (a Raspberry Pi running
  [NetKey](https://netkey.no)) and sends you an invite code, or a public party from the list in the app.

You don't need a Raspberry Pi yourself to join. Only the host does.

## Quick start

1. **Install NetKey.** Download `NetKey-<version>-setup.exe` from [Releases](../../releases) and run it.
   Windows may say "Windows protected your PC", because the installer isn't code-signed yet (NetKey is free and
   non-profit): click **More info → Run anyway**.
2. **Set up your emulator once.** Open NetKey, go to **Emulators**, and follow the steps for yours (see below).
3. **Join a party.** Paste the invite code from your host, or go to **Find a party**. The host approves you.
4. **Play.** Start your game in the emulator and pick the network or IPX game in its multiplayer menu.
   The other players are there.

NetKey keeps running in the notification area when you close the window, so your party stays connected.

![In a party: who's playing, whether your emulators are sending traffic, and the party chat](docs/screenshot-party.png)

## Playing DOS games in DOSBox (the easy way)

Most DOS network games use **IPX**: Doom, Duke Nukem 3D, Warcraft II, Command & Conquer, Descent, Rise of the
Triad… Every DOSBox (DOSBox, DOSBox-X and DOSBox Staging) can carry IPX to NetKey by itself. Nothing to install, no
drivers in DOS, and **no ports to open on anyone's router**.

### Why this is easier than the usual way

DOSBox has always been able to play IPX games over the internet: one player runs `IPXNET STARTSERVER`, or everyone
connects to a public IPX server. NetKey runs that server on your own PC and carries the IPX through the party
instead:

|  | The usual way | With NetKey |
|---|---|---|
| Router settings | The host forwards UDP port 213, or everyone uses a server on the internet | **None.** Works behind any home router, even mobile and CGNAT connections |
| Who can join | Anyone who knows the address | **Only players the host lets in**, with an invite code and a key check |
| Encryption | None: IPX travels in the open | **Encrypted** end to end (WireGuard) |
| Who you can play with | Other DOSBox players | DOSBox players **and real retro PCs, VMs and 86Box** in the same party |
| IPX frame types | Not an issue (DOSBox only) | NetKey answers every machine in its own frame type: nothing to set |
| Extras | | Party chat, public party list, a connection test, Network Neighborhood for the VMs |

Your DOSBox talks only to NetKey on your own PC (`127.0.0.1`); nothing on your network or the internet can reach
that IPX server.

![The Emulators page walks you through it, with settings to copy](docs/screenshot-dosbox.png)

**1. Turn on IPX.** Open your DOSBox config file (`dosbox-x.conf`, `dosbox.conf` or `dosbox-staging.conf`; in
DOSBox-X the Start menu has "View or edit dosbox-x.conf"), find the `[ipx]` section and change `false` to `true`:

```ini
[ipx]
ipx = true
```

Then start DOSBox again: it reads the config when it starts. Until then, `IPXNET` is an "unknown command".

**2. Connect DOSBox to NetKey.** Join the party in NetKey, then type this in DOSBox (NetKey runs on the same PC):

```
IPXNET CONNECT 127.0.0.1
```

DOSBox answers that it's connected, and NetKey's Party page shows "1 DOSBox connected". Tip: put that line in the
`[autoexec]` section of the config, so DOSBox connects by itself every time.

**3. Play.** Start your game and choose an IPX network game. A few examples:

- **Doom / Doom II:** `IPXSETUP -nodes 2` (the number of players).
- **Duke Nukem 3D:** run `SETUP`, choose *Network game*, then start a game from `SETUP`.
- **Warcraft II:** *Multi Player Game → IPX Network*.
- **Command & Conquer:** *Multiplayer Game → Network*.

Use `IPXNET CONNECT`, not `IPXNET STARTSERVER`: NetKey is the server. Two DOSBox windows on the same PC can also play
each other this way, which is a handy test.

### Advanced: DOSBox-X with a full network card

Only needed for DOS programs that use NetBEUI or TCP/IP instead of IPX. DOSBox-X's emulated NE2000 card connects to
the NetKey Adapter through Npcap:

1. Install [Npcap](https://npcap.com) with **"Install Npcap in WinPcap API-compatible Mode"** ticked.
2. In NetKey, open **Emulators → DOSBox-X: full network card** and copy the settings into `dosbox-x.conf`. They give
   DOSBox-X its own network address (every DOSBox-X has the same one by default, and two in a party can't see each
   other):

   ```ini
   [ne2000]
   ne2000=true
   nicbase=300
   nicirq=10
   macaddr=AC:DE:48:xx:xx:xx
   backend=pcap

   [ethernet, pcap]
   realnic=NetKey
   ```

3. In DOS, load the drivers your program needs: for IPX, Novell's `LSL`, `NE2000` and `IPXODI` with a `NET.CFG`
   (`FRAME Ethernet_802.2`; see the
   [DOSBox-X networking guide](https://dosbox-x.com/wiki/Guide%3ASetting-up-networking-in-DOSBox%E2%80%90X)); for
   TCP/IP tools like mTCP, DOSBox-X's built-in packet driver (`NE2000 0x60 10 0x300`).

## Windows games in VMware or VirtualBox

If your game needs Windows 95, 98 or XP, run that Windows in a virtual machine and connect it to the party:

- **VMware Workstation:** *Edit → Virtual Network Editor → Change Settings → Add Network* (for example VMnet2),
  set it to **Bridged** to **NetKey Adapter**. Then in your virtual machine's settings set the network adapter to
  *Custom: VMnet2*.
- **VirtualBox:** *Settings → Network → Attached to: Bridged Adapter → NetKey Adapter*.

The virtual machine gets its address from the party host, like a PC plugged into their network. Inside Windows, add
the **IPX/SPX protocol** to the network card if your game uses IPX (most 90s games do).

## 86Box

In 86Box, open *Settings → Network*, set the mode to **Local Switch**, and paste the **shared secret** from
NetKey's Emulators page. Nothing else to install.

## Is my real PC on the party?

No. Only your emulators and virtual machines join the party's network. Windows itself has its networking turned off
on the NetKey Adapter, so other players can't see your PC, your files or your printers.

## Questions

- **Is it free?** Yes, NetKey is free and non-profit.
- **Do I need a Raspberry Pi?** Not to join. The host runs NetKey on a Raspberry Pi (a NetKey box), and that's what
  makes the party. See [netkey.no](https://netkey.no) if you want to host one.
- **My friend can't see my game.** Check that NetKey's Party page shows your DOSBox as connected, and use the
  **Run test** button to check your connection. Real retro PCs and virtual machines in the party should use the IPX
  frame type `Ethernet_802.2`; DOSBox players don't need to set anything.
- **Can I host a party from this app?** Not yet: hosting needs a NetKey box.

## For developers

```
service/     netkeyd, the Windows service (Go): hub client, WireGuard (wireguard-go with its own TCP/IP stack),
             VXLAN to the party host, the switch, the NetKey Adapter, 86Box Local Switch and DOSBox IPX ports,
             the named pipe the app talks to, install/uninstall
docs/        screenshots and design notes (docs/design-dosbox-ipx.md)
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
service and the app, and writes `stage\out\NetKey-<version>-setup.exe`, plus the TAP-Windows6 source archive to
attach to each release.

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
