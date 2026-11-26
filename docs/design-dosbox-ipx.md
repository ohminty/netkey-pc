# Design: IPX for DOSBox, without Npcap

Status: implemented (service/internal/ipx); revised after review, see "Review" at the end

## Goal

Let DOS games in **any DOSBox** (DOSBox, DOSBox-X, DOSBox Staging) play IPX games in a NetKey party with nothing
to install and no DOS drivers:

```
[ipx]
ipx=true
```
```
IPXNET CONNECT 127.0.0.1
IPXSETUP -nodes 2
```

Today the only way is DOSBox-X's NE2000 card, which needs Npcap (WinPcap mode) on the host and Novell's
LSL/NE2000/IPXODI drivers plus a NET.CFG in DOS. That's too much for the novice users we want.

Non-goals: NetBEUI and TCP/IP for DOS programs (they keep using the NE2000 card); DOSBox on other machines in the
home network (loopback only, see Security).

## How DOSBox's IPX tunnel works

(From DOSBox-X `src/hardware/ipx.cpp` and `ipxserver.cpp`.) DOSBox emulates the DOS IPX driver (INT 7A / 2F
AX=7A00) itself and carries every IPX packet in one UDP datagram to an "IPX server": the 30-byte IPX header
(checksum 0xFFFF, big-endian length and sockets) followed by the data.

- **Registration** (`IPXNET CONNECT host [port]`, default port 213): the client sends a bare 30-byte header with
  dest socket 2, dest network 0, dest node 0 (host 0, port 0), src socket 2. DOSBox-X with an NE2000 card appends its
  6-byte MAC and sets transport control to `'M'` (36 bytes). It waits up to 5 s; **any** reply ends the wait. It
  takes its own node address from the reply's dest node and its network number from the reply's dest network.
- **Server reply**: checksum 0xFFFF, length 30, dest network 0, dest node = the client's node, dest socket 2,
  src network 1, src node = server address, src socket 2.
- **Sending**: the client fills in src network/node/socket itself; dest network is whatever the game set (usually 0).
- **Receiving**: the client does **not** check the dest node: every packet goes to the DOS program listening on
  the dest socket. The server must route precisely.
- **Broadcast**: dest node FF:FF:FF:FF:FF:FF (DOSBox's server checks the first four bytes).
- **Ping** (`IPXNET PING`): a broadcast to socket 2; every client answers the pinger with a unicast to socket 2.
- **No keepalive**: DOSBox's own server never forgets a client; clients only notice a failed send.

## Design

A new switch port in netkeyd, **`ipx`**, next to `tap` (NetKey Adapter) and `86box` (Local Switch). It is an IPX
server for DOSBox on the same PC, and translates between DOSBox's UDP packets and real IPX frames on the party.

```
 DOSBox ─UDP 127.0.0.1:213─┐
 DOSBox ─UDP──────────────┤ ipx port ── switch ── party (VXLAN) ── host's LAN: real PCs, VMs, other DOSBoxes
                           │   (one MAC per DOSBox)    └─ tap (VMware, DOSBox-X NE2000), 86box
```

### Node addresses = MAC addresses

Each registered DOSBox gets a node address that is also its MAC on the party, because on Ethernet an IPX node *is*
the station's MAC: a random locally administered address `02:4E:4B:xx:xx:xx` ("NK"), kept for the client's UDP
address. A DOSBox-X `'M'` registration's MAC is ignored (see Review 2).

The switch learns these MACs on the `ipx` port like any other. A clash with another station is reported by the
switch's existing duplicate-MAC warning.

### From DOSBox to the party

For each datagram from a registered client:
1. Validate: at least 30 bytes, length field matches, src node equals the client's node (otherwise drop: a game
   can't spoof another station).
2. **Socket 2** (registration, ping, ping replies) is DOSBox's own control traffic: handled locally, never sent to
   the party (on a real LAN socket 2 is Novell's echo socket).
3. **Local delivery**: broadcast → every other local client; unicast to another local client → only that client.
4. **To the party** (broadcast, or unicast to a node that isn't local): wrap the IPX packet in an Ethernet frame of
   the party's frame type, dst MAC = dest node, src MAC = client node, and hand it to the switch.

### From the party to DOSBox

The switch gives the port every frame for its MACs plus broadcasts. For each:
1. Decode IPX from any of the four framings (Ethernet II 0x8137, 802.3 raw FFFF, 802.2 E0E0, SNAP AAAA…8137).
2. Deliver to the client whose node is the dest node, or to all clients for a broadcast. Drop socket 2.

### Frame type

Real PCs only see IPX in their own frame type (DOS Novell stacks bind one; Windows "Auto" settles on one). So:
- **Broadcasts** go out in Ethernet 802.2 (the Novell default since NetWare 4, and what NetKey's guides use) **and**
  in every other IPX frame type seen from the party in the last 5 minutes. A station ignores the copies that aren't
  its frame type, so each one sees the broadcast exactly once.
- **Unicasts** go out in the frame type last seen from that destination MAC (802.2 if never seen).
- Nothing to configure.

### Registration and expiry

- Registration from a known UDP address keeps its node (DOSBox reconnecting). At most 32 clients.
- DOSBox has no keepalive, so clients are only forgotten after 30 minutes without a packet.
- **Adoption**: a packet from an unregistered loopback address with a plausible src node (not zero, not broadcast,
  not taken) registers that client with that node. So DOSBox sessions survive a service restart without
  `IPXNET CONNECT` again.

### Lifecycle

The server listens from service start, in and out of parties, so DOSBox can connect before the party starts; frames
simply go nowhere outside a party (the party port only exists in one). If port 213 is taken, the status says so.

### Security

Listens on **127.0.0.1 only** and only accepts datagrams from loopback addresses. DOSBox's IPX tunnel has no
authentication or encryption; anything on the home network could otherwise inject IPX into the party. (DOSBox on
another home machine stays a later, opt-in feature.)

### Status and app

`status` gets `ipx: {listening, port, error, clients: [{node, since}]}`; traffic comes from the switch counters of the
`ipx` port, like the other ports. The app:
- "Your emulators" gets a row **DOSBox IPX**: "2 DOSBox connected · traffic flowing" / "Waiting for DOSBox".
- Emulators page: a new first card **DOSBox (IPX, easiest)** with the two config lines and `IPXNET CONNECT
  127.0.0.1`, before the DOSBox-X NE2000 card (renamed "DOSBox-X network card (advanced, Npcap)").

### Tests

- Registration reply bytes; DOSBox-X `'M'` registration takes the MAC; re-registration keeps the node.
- Routing: broadcast to locals and party, unicast local vs party, socket 2 never leaves, spoofed src dropped.
- Framing: encode/decode all four frame types; broadcasts in every frame type seen; unicast in the destination's
  type; oversize dropped per type.
- Expiry and adoption.
- On the PC: two DOSBox-X windows `IPXSETUP -nodes 2` (local path), and frames seen on netkey3 (party path).

## Review

What the review of draft 1 changed:

1. **Frame type: "follow the majority" was fragile.** Windows XP with frame type Auto probes all four types at
   start-up (we captured exactly that from the XP VM), which would swing a majority count, and a party mixing frame
   types would always leave someone out. Now broadcasts go out in every frame type seen and unicasts in the
   destination's own type, so a mixed party just works and there's no setting.
2. **Don't reuse DOSBox-X's NE2000 MAC as the node.** Someone using the NE2000 card on the NetKey Adapter *and* the
   IPX tunnel in the same DOSBox-X would have one MAC on two switch ports: the switch would flap between them and
   raise a false duplicate warning. The client takes its node from our reply, so we always hand out our own.
3. **Thread safety.** The switch calls a port's `Send` from whichever port received the frame (party, adapter,
   Local Switch) while the UDP reader registers clients: the client table needs a lock, and delivery must not hold
   it while sending.
4. **Oversize packets are per frame type**: the largest IPX packet is 1500 bytes in Ethernet II and raw 802.3, 1497
   in 802.2 (3-byte LLC) and 1492 in SNAP (8 bytes). A packet too big for one of the broadcast copies skips only
   that copy.
5. **Limits**: at most 32 clients (a table full of adopted addresses can't grow without bound).
6. **Guide wording**: say *connect*, never `IPXNET STARTSERVER`: port 213 belongs to NetKey, and a DOSBox trying to
   be the server would fail to bind it. Two DOSBox windows on one PC can also play together through NetKey, even
   outside a party, which is a nice way to test.

Kept as designed: loopback only; socket 2 stays local; adoption after a service restart; 30-minute expiry; the server
listens outside parties too.
