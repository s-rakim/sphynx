# Sphynx

Sphynx is your own private network, like Tailscale, running on your own PC in
Docker. Every phone, laptop, tablet, TV and server you own gets a private
address and can reach the others from anywhere, through an encrypted
WireGuard tunnel, with the official Tailscale apps. There is no account with
anyone, no subscription and no device limit.

What you get:

- **A web admin page** to add devices, see who's online, approve sign-ins,
  manage users, access rules, DNS, keys and routes. It works on a phone too.
- **Every Tailscale feature the open-source server supports**: MagicDNS,
  subnet routers, exit nodes, access rules (ACLs and grants), tags, auth
  keys, Tailscale SSH, Taildrop, Taildrive, single sign-on, peer relays and
  more. [docs/FEATURES.md](docs/FEATURES.md) goes through Tailscale's
  documentation feature by feature.
- **Sphynx home**: the PC running Sphynx is an exit node out of the box, so
  any device can use your home internet connection, and it can share your
  home network (printer, NAS, router page) with your devices.
- **Wake-on-LAN**: switch on a sleeping or powered-off computer at home from
  anywhere.
- **Sphynx Chat**: a WhatsApp-style messenger in which every device on your
  network is its own contact, with end-to-end encrypted chats, groups,
  photos, files, voice notes and calls.
- **Weak-signal mode**: keeps devices connected on bad mobile networks and
  through strict firewalls.
- **Sphynx Link**: a backup way home that needs no port forwarding and no
  VPN app, built on Tailscale's Tailcat.
- **Public shares**: publish one web service from your network on the
  internet, like Tailscale Funnel.

Sphynx is built on [Headscale](https://github.com/juanfont/headscale) (the
open-source Tailscale control server) and the official
[Tailscale](https://github.com/tailscale/tailscale) clients, pulled in
unmodified at build time. See [License and credits](#license-and-credits).

## Contents

1. [How it works](#how-it-works)
2. [Install Sphynx on your PC](#1-install-sphynx-on-your-pc)
3. [First start](#2-first-start)
4. [Make Sphynx reachable from anywhere](#3-make-sphynx-reachable-from-anywhere)
5. [Connect your devices](#4-connect-your-devices) (Windows, Mac, iPhone and iPad, Android, Linux, Apple TV, NAS and Docker)
6. [Use your home internet from anywhere (exit node)](#5-use-your-home-internet-from-anywhere-exit-node)
7. [Reach your home network](#6-reach-your-home-network)
8. [Wake-on-LAN](#7-wake-on-lan)
9. [Sphynx Chat](#8-sphynx-chat)
10. [Weak-signal mode](#9-weak-signal-mode)
11. [Sphynx Link](#10-sphynx-link)
12. [Public shares](#11-public-shares)
13. [Everything else](#12-everything-else)
14. [Backup, update and uninstall](#backup-update-and-uninstall)
15. [Troubleshooting](#troubleshooting)
16. [Security notes](#security-notes)
17. [License and credits](#license-and-credits)

## How it works

```
 your phone ─┐                       ┌─ your PC at home (Docker)
 your laptop ┼── encrypted tunnels ──┤   ├─ Sphynx admin page   :3000
 your tablet ┘   (WireGuard)         │   ├─ Headscale (control server)
                                     │   ├─ Sphynx home (exit node, home network, chat)
                                     │   └─ Sphynx Link (Tailcat)
```

The control server only introduces devices to each other. Your traffic goes
directly between devices when it can, or through an encrypted relay when a
direct path is blocked. The relays never see inside the tunnels.

## 1. Install Sphynx on your PC

You need a PC that stays on (Windows 10/11, macOS or Linux) and about 1 GB of
disk space.

1. Install Docker:
   - **Windows**: install [Docker Desktop](https://www.docker.com/products/docker-desktop/).
     When it asks, keep "Use WSL 2" selected. Restart if asked, then open
     Docker Desktop once and wait until it says "Engine running". In
     Docker Desktop's Settings → General, turn on "Start Docker Desktop
     when you sign in to your computer", so Sphynx comes back after a restart.
   - **macOS**: install [Docker Desktop](https://www.docker.com/products/docker-desktop/) and open it once.
   - **Linux**: install Docker Engine and the compose plugin, for example
     `curl -fsSL https://get.docker.com | sh`.
2. Get Sphynx. Either download this repository as a ZIP (green **Code**
   button → **Download ZIP**) and unzip it, or with Git:

   ```
   git clone https://github.com/s-rakim/sphynx.git
   ```

3. Open a terminal in the `sphynx` folder (on Windows: open the folder in
   File Explorer, click the address bar, type `powershell` and press Enter)
   and start it:

   ```
   docker compose up -d --build
   ```

   The first build takes a few minutes. After that Sphynx starts by itself
   whenever Docker does.

## 2. First start

1. On the PC, open **http://localhost:3000**.
2. Choose an admin password (at least 8 characters). You'll use it to sign in
   to this page and to approve new devices.
3. Sphynx asks two things:
   - **Server address**: the address your devices use to reach Sphynx. To try
     things out at home, use the PC's address on your home network with port
     8080, like `http://192.168.1.20:8080`. On Windows, run `ipconfig` and use
     the "IPv4 Address" of your Wi-Fi or Ethernet adapter. To use Sphynx away
     from home (the usual case), keep reading: you'll change this to an
     `https://` address in the next step.
   - **Your user name**: your devices belong to this user, for example
     `malcolm`.
4. A minute later the Overview page shows **Sphynx home** as connected and
   ready as an exit node.

Windows may ask whether Docker Desktop may use the network. Allow it on
private networks.

Tip: give the PC a fixed address on your home network (in your router, look
for "DHCP reservation" or "static lease"), so the address doesn't change.

## 3. Make Sphynx reachable from anywhere

Do this before adding devices that leave the house. iPhones and iPads also
need it: the iOS Tailscale app may refuse a plain `http://` server.

You need three things: a name for your home connection, a few ports forwarded
on your router, and HTTPS turned on in Sphynx.

### 3.1 Get a free name for your home connection

Your home internet address changes from time to time, so give it a name that
follows it. [DuckDNS](https://www.duckdns.org) is free:

1. Sign in at duckdns.org and add a subdomain, for example `myname`. You now
   have `myname.duckdns.org`.
2. Keep it pointing at your home. The simplest way is to run DuckDNS's
   updater next to Sphynx. In the `sphynx` folder, create a file named
   `docker-compose.override.yml` with:

   ```yaml
   services:
     duckdns:
       image: lscr.io/linuxserver/duckdns:latest
       restart: unless-stopped
       environment:
         SUBDOMAINS: myname
         TOKEN: your-duckdns-token
   ```

   (Your token is shown at the top of the DuckDNS page.) Many routers can also
   update DuckDNS themselves under "Dynamic DNS".

Every name under yours, like `chat.myname.duckdns.org`, automatically points
to the same place. Sphynx Chat uses that (see [Sphynx Chat](#8-sphynx-chat)).

Any other domain name works too: point it (and `chat.` in front of it) at your
home's public IP address.

### 3.2 Forward ports on your router

In your router's settings (usually http://192.168.1.1 or http://192.168.0.1,
look for "Port forwarding", "Virtual server" or "NAT"), forward these to the
PC's home network address:

| Port | Protocol | What for |
|---|---|---|
| 443 | TCP | Devices, the built-in relay, Sphynx Chat over HTTPS, public shares |
| 80 | TCP | Getting HTTPS certificates from Let's Encrypt |
| 3478 | UDP | Helps devices find direct paths (STUN) |
| 41641 | UDP | Fast direct connections to Sphynx home (exit node, home network) |

If your router's internet address starts with 100.64 to 100.127, or differs
from what [whatismyip.com](https://www.whatismyip.com) shows, your provider
uses carrier-grade NAT and port forwarding can't work. Ask your provider for a
public IP address, or use [Sphynx Link](#10-sphynx-link), which needs no open
ports.

### 3.3 Start Sphynx with the internet ports and turn on HTTPS

1. Restart Sphynx with the extra ports:

   ```
   docker compose -f docker-compose.yml -f docker-compose.internet.yml up -d --build
   ```

   (If you created `docker-compose.override.yml` for DuckDNS, add
   `-f docker-compose.override.yml` to the end as well.)

2. In the admin page, open **Settings**:
   - **Server address**: `https://myname.duckdns.org`
   - **HTTPS mode**: Let's Encrypt. **Domain name**: `myname.duckdns.org`,
     **Email**: yours (Let's Encrypt writes before a certificate expires).
   - **Relays**: turn on **Built-in relay on this server**. Keep Tailscale's
     public relays on too, as a fallback.
   - Click **Save**.
3. Check it from your phone with Wi-Fi turned off: open
   `https://myname.duckdns.org/health`. It should say `pass`.

## 4. Connect your devices

Every device runs the official Tailscale app, pointed at your Sphynx server
instead of Tailscale's. In the admin page, **Add device** shows these steps
with your own server address filled in, and can create a key that connects a
device without typing your password.

When a device connects without a key, a Sphynx page opens: enter your admin
password and click **Approve device**. The device shows up on the **Devices**
page within seconds, where you can rename it.

Below, `https://myname.duckdns.org` stands for your server address.

### Windows

1. Install Tailscale from [tailscale.com/download/windows](https://tailscale.com/download/windows). Don't sign in in the app.
2. Open PowerShell or Command Prompt and run:

   ```
   tailscale login --login-server https://myname.duckdns.org
   ```

3. A browser page opens: enter your Sphynx admin password and click **Approve device**.
4. Optional: in the tray icon's **Preferences**, turn on **Run unattended**, so
   it stays connected while you're signed out of Windows.

### macOS

1. Install Tailscale from the Mac App Store or [tailscale.com/download/mac](https://tailscale.com/download/mac).
2. Hold the Option (⌥) key and click the Tailscale icon in the menu bar. Open
   **Debug → Custom Login Server → Add Account…** and enter your server address.
3. A browser page opens: enter your admin password and click **Approve device**.

### iPhone and iPad

1. Install **Tailscale** from the [App Store](https://apps.apple.com/app/tailscale/id1470499037).
2. Open it, tap the account icon at the top right, then **Log in…**.
3. Tap the **⋯** menu at the top right, choose **Use custom coordination
   server** and enter `https://myname.duckdns.org`.
4. A Sphynx page opens: enter your admin password and tap **Approve device**.
5. Allow the VPN configuration when iOS asks.

### Android

1. Install **Tailscale** from [Google Play](https://play.google.com/store/apps/details?id=com.tailscale.ipn) or F-Droid.
2. Open it, tap the settings icon at the top right, then **Accounts**.
3. Tap **⋮** at the top right, choose **Use an alternate server** and enter
   `https://myname.duckdns.org`.
4. A Sphynx page opens: enter your admin password and tap **Approve device**.
   (With a key from **Add device** instead: close the login page, go back to
   **Accounts → ⋮ → Use an auth key** and paste the key.)
5. Allow the VPN connection when Android asks.

On both iPhone and Android, keep **Use Tailscale DNS** on (it is by default),
so names like `sphynx-home` work.

### Linux

```
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up --login-server https://myname.duckdns.org
```

It prints a link: open it in any browser and approve with your admin
password. To share this machine's internet or network, see
[exit nodes](#5-use-your-home-internet-from-anywhere-exit-node) and
[routes](#6-reach-your-home-network).

### Apple TV

1. Install Tailscale from the App Store on the Apple TV. Don't sign in yet.
2. Open it, choose **Install VPN configuration** and allow it.
3. Open the Apple TV's **Settings → Apps → Tailscale → Alternate coordination
   server URL** and enter your server address.
4. Go back to Tailscale and scan the QR code with your phone. On the page that
   opens, enter your admin password and tap **Approve device**.

### NAS, Raspberry Pi and Docker

Synology, QNAP and TrueNAS have Tailscale packages: use their "custom login
server" option, or their command line with `--login-server`. In Docker, use
the official image (the **Add device** page has a ready-to-copy command with a
key):

```
docker run -d --name tailscale --restart unless-stopped \
  --cap-add NET_ADMIN --cap-add NET_RAW --device /dev/net/tun \
  -v tailscale-state:/var/lib/tailscale --network host \
  -e TS_STATE_DIR=/var/lib/tailscale -e TS_AUTHKEY=<key from Add device> \
  -e TS_EXTRA_ARGS=--login-server=https://myname.duckdns.org \
  tailscale/tailscale
```

### Using your devices

Once connected, devices reach each other by name: `ping laptop`,
`http://nas:5000`, or `laptop.sphynx.internal` (the full name; the domain is
set under **DNS**). The admin page itself is at **http://sphynx-home:3000**
from any of your devices.

## 5. Use your home internet from anywhere (exit node)

Sphynx home is an exit node from the start: your traffic leaves through your
home connection, as if you were at home. Useful on public Wi-Fi, or to use
home-only services while away.

- **Phone**: in the Tailscale app, tap **Exit Node** and pick **sphynx-home**.
- **Windows and Mac**: Tailscale icon → **Exit nodes** → **sphynx-home**.
- **Linux**: `sudo tailscale set --exit-node=sphynx-home`.

Turn on **Allow local network access** in the same menu if you still want to
reach the printer at the café or hotel.

Any other device can be an exit node too, for example a computer at a
relative's house: on it, choose "Run as exit node" in the Tailscale menu (on
Linux `sudo tailscale set --advertise-exit-node`), then approve it under
**Exit nodes & routes**. Weak-signal mode can also pick the best exit node
automatically.

## 6. Reach your home network

To reach devices at home that don't run Tailscale (printer, NAS, cameras, the
router's own page):

1. **Settings → Sphynx home → Home networks to share**: enter your home
   network, like `192.168.1.0/24` (the first three numbers of your PC's
   address, then `.0/24`). Save.
2. On Windows, Mac, iPhone and Android, using shared networks is on by
   default. On Linux run `sudo tailscale set --accept-routes`.

Now `http://192.168.1.1` works from your phone anywhere. Other devices can
share their own networks the same way (Linux:
`sudo tailscale set --advertise-routes=10.0.0.0/24`), approved under
**Exit nodes & routes**.

## 7. Wake-on-LAN

Switch on a sleeping or powered-off computer at home from anywhere.

1. Prepare the computer to wake:
   - In its BIOS/UEFI, turn on "Wake on LAN" (sometimes "Power on by PCI-E").
   - On Windows: **Device Manager → your Ethernet adapter → Properties →
     Power Management**: allow it to wake the computer, and under **Advanced**
     enable **Wake on Magic Packet**. Turning off Windows "Fast startup" helps.
   - Use a cable. Most computers can't be woken over Wi-Fi.
2. Find its MAC address (on Windows: `ipconfig /all`, "Physical Address" of
   the Ethernet adapter).
3. In the admin page, open **Wake-on-LAN → Add a computer to wake**: give it a
   name, enter the MAC address, and pick an always-on device on the same home
   network to send the wake signal (a Raspberry Pi, NAS or another PC running
   Tailscale, owned by your user rather than tagged). That's the reliable way
   with Docker Desktop, which can't broadcast onto your home network itself.
4. Click **Wake**. You can do this from your phone through
   http://sphynx-home:3000.

## 8. Sphynx Chat

A WhatsApp-style messenger for your Sphynx network. **Every device is its own
contact with its own chat**, named like on the Devices page: your phone, your
laptop and your tablet each have their own conversation, just like different
people in WhatsApp. It has chats, groups, communities, channels, status
updates, photos, videos, files, voice notes, reactions, replies, polls,
disappearing messages and voice and video calls. Messages are end-to-end
encrypted in the browser; the server stores only ciphertext.

### Open it

- **On a phone, tablet or computer**: connect it to Sphynx, then open
  **http://sphynx-home/** in its browser (or **http://100.64.0.1/**, if
  "Use Tailscale DNS" is off). On a phone, use the browser's **Add to Home
  Screen** to get an app icon.
- **On the PC that runs Sphynx**: open **http://localhost:3001**. Sign in to
  the admin page first; on the PC you chat as Sphynx home.

The chat list shows your conversations first, then **Other devices on your
network**. Tap a device to start chatting. Devices that haven't opened the
chat yet are greyed out: messages are encrypted for each device's own keys,
which it makes the first time it opens the chat. You can give yourself a chat
name of your own under **Settings → Profile**; others still see the device's
network name under it.

### Calls, voice notes and notifications need HTTPS

Browsers only allow the camera, microphone, location and notifications on
secure (HTTPS) pages. On http://sphynx-home/ everything else works, and the
chat brings its own encryption code, because browsers also hold back their
built-in encryption on plain http.

Once HTTPS is on (step 3.3), Sphynx also serves the chat at
**https://chat.myname.duckdns.org/**:

1. The name must point to your home, like your server's name. With DuckDNS
   it already does. With your own domain, add `chat.` in front of it at your
   DNS provider.
2. Ports 80 and 443 must be forwarded (step 3.2).
3. The admin page's **Chat** page shows the HTTPS address as **Ready** once
   Sphynx has its certificate. Your devices then reach that name through your
   Sphynx network automatically.

New browsers that open http://sphynx-home/ move to the HTTPS address by
themselves. A browser already set up on http shows a banner with the link;
there it links to your chats again with new keys (messages from before stay
readable in the old browser).

The **Chat** page in the admin page also turns the chat on or off and sets
the largest file people can send.

### Good to know

- Each browser you use is a "linked browser" of its device (**Settings →
  Linked browsers**). Messages sent before a browser was linked stay
  unreadable in it, like a new WhatsApp linked device.
- Compare security codes (tap the lock note at the top of a chat) to check
  that nobody, including whoever runs the server, can read your messages.
- Sphynx Chat is based on Tailchat. Tailchat's command-line clients are not
  part of Sphynx.

## 9. Weak-signal mode

Under **Settings → Weak-signal mode** (all but the last are on by default):

- **Reconnect without the server**: devices remember each other and keep
  working even when your home server is unreachable for a while.
- **Trust working tunnels**: keep using a tunnel that still works, even if the
  server can't see the other device right now.
- **Keep direct paths alive on cellular**: stops mobile networks from quietly
  dropping direct connections.
- **Automatic exit node**: let devices pick the best exit node by themselves.
- **Packet size discovery**: finds the packet size each path allows, which
  helps on unusual mobile and satellite links. Experimental in Tailscale.

For a device stuck behind a strict firewall that only allows HTTPS, open it on
the **Devices** page and turn on **Strict firewall**: it then uses only
TCP port 443.

**Sphynx home → Peer relay UDP port** makes Sphynx home a fast UDP relay for
devices that can't reach each other directly (forward that UDP port too).

## 10. Sphynx Link

A second way home that needs no port forwarding and no VPN app, built on
[Tailcat](https://github.com/tailscale/tailcat) by Tailscale. Useful when your
provider uses carrier-grade NAT, or on a computer where you can't install
anything.

1. Open **Sphynx Link**, turn it on, pick the services (your home internet,
   the inbox for files, this admin page) and save. You get a secret address.
2. On another computer, download the small `tailcat` program from
   [its releases page](https://github.com/tailscale/tailcat/releases) (no
   install or admin rights needed). The Sphynx Link page shows the exact
   commands, for example a SOCKS5 proxy for your browser that uses your home
   internet:

   ```
   tailcat socks --listen=127.0.0.1:1080 <your link address>
   ```

Treat the address like a password, or limit the link to specific computers
under "Only allow certain computers". Sphynx Link needs your PC to reach the
internet.

## 11. Public shares

Publish one web service from a device on your network on the internet, at
`https://myname.duckdns.org/s/<name>/`, like Tailscale Funnel. Open **Public
shares**, allow them, then add a share: a name, the device and its port.
Anyone on the internet can open these addresses, so only share services that
have their own login.

## 12. Everything else

- **Devices**: rename, tag, expire, remove; see each device's connection
  (direct, peer relay or relayed) and its exit node and routes.
- **Users**: add people; each person's devices belong to them. Single sign-on
  with Google, Microsoft, Authentik and others is under **Settings → Single
  sign-on**.
- **Access controls**: Tailscale's policy file (grants, ACLs, groups, tags,
  auto-approvers, SSH rules, tests). The default lets every device reach
  every other device; Sphynx adds its own rules (shown on the page) for
  Sphynx home, the chat and weak-signal mode.
- **DNS**: MagicDNS, your own DNS servers (for example a Pi-hole on your
  network for ad blocking everywhere), split DNS, search domains and extra
  DNS records.
- **Keys**: auth keys for adding devices without a password (reusable,
  ephemeral, pre-tagged) and API keys for Headscale's API.
- **Tailscale SSH**: log in to your own Linux devices without SSH keys:
  `tailscale ssh user@laptop` (turn it on the device with
  `sudo tailscale set --ssh`). For rules with `"action": "check"`, the
  terminal shows a link: open it and confirm with your admin password.
- **Taildrop**: send files between your devices from the share menu or
  `tailscale file cp`.
- **Taildrive**: share folders between your devices.
- **Logs**: what Sphynx, Headscale and Sphynx home are doing.
- **Headscale's command line**, for anything the admin page doesn't show:
  `docker exec sphynx headscale --help`.

## Backup, update and uninstall

**Backup**: **Settings → Backup & maintenance → Download backup** saves
Sphynx's settings, keys and database, and the chat database, as a ZIP file.
Keep it somewhere safe: it can impersonate your server. Chat attachments are
not included (they are encrypted and can be large).

**Restore** a backup (PowerShell, in the `sphynx` folder, with the ZIP file
there too):

```
docker compose down
docker run --rm -v sphynx_sphynx-data:/data -v "${PWD}:/backup" alpine sh -c "cd /data && unzip -o /backup/sphynx-backup-2026-10-30.zip"
docker compose up -d
```

**Update**: download the new version (or `git pull`), then run the same
`docker compose ... up -d --build` command you used to start it. Your
settings and devices are kept in the `sphynx-data` Docker volume.

**Uninstall**: `docker compose down`, and `docker volume rm sphynx_sphynx-data`
to delete all data. On each device, log out of the server in the Tailscale
app.

## Troubleshooting

- **The admin page doesn't open**: check that Docker Desktop is running and
  that `docker compose ps` shows `sphynx` as "healthy". It only opens on the PC
  itself (http://localhost:3000) or, once connected, at
  http://sphynx-home:3000.
- **A device doesn't connect**: check the server address in the app (it must
  match **Settings → Server address** exactly), that `/health` opens from that
  device, and the router's port forwards. iPhones and iPads need an `https://`
  address. Look at **Logs** for the device's attempt.
- **Connected, but slow**: the Devices page shows "Relayed" when no direct
  path was found. Forward UDP 41641 and 3478, or set a peer relay port.
- **`sphynx-home` doesn't resolve**: turn on **Use Tailscale DNS** in the app
  (Linux: `sudo tailscale set --accept-dns`), or use `100.64.0.1`.
- **The chat says to connect this device**: open it from a device connected
  to Sphynx. On the PC itself, use http://localhost:3001 after signing in to
  the admin page.
- **The chat's HTTPS address isn't ready**: the **Chat** page shows the last
  error. Usually `chat.<your domain>` doesn't point to your home yet, or port
  80 isn't forwarded.
- **Wake-on-LAN doesn't wake the computer**: wake it while it's asleep (not
  unplugged), over a cable, and make sure the sending device is online and on
  the same home network.
- **Start over with a device**: remove it on the Devices page, log out in the
  Tailscale app and connect again.

## Security notes

- The admin page is only published on the PC itself (`127.0.0.1:3000`) and on
  your Sphynx network. Use a strong admin password: it also approves new
  devices.
- Device traffic is end-to-end encrypted with WireGuard. The relays, your
  router and your internet provider can't read it.
- Sphynx Chat messages are encrypted in each browser; the Sphynx server stores
  only ciphertext. On plain http pages the chat uses its own copy of the same
  encryption (built on the audited noble libraries), and the page could read
  its own private key, which the browser's built-in tools prevent; the HTTPS
  address avoids that.
- Keep Sphynx updated, and keep backups private.

## License and credits

Sphynx's own code is under the [BSD 3-Clause License](LICENSE).

Sphynx bundles, unmodified and pulled from their official releases at build
time: [Headscale](https://github.com/juanfont/headscale),
[Tailscale](https://github.com/tailscale/tailscale) and
[Tailcat](https://github.com/tailscale/tailcat), all under the BSD 3-Clause
License. Their notices, and those of the libraries Sphynx is built with, are in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and inside the Docker image
under `/usr/share/doc/sphynx/`.

Tailscale, WireGuard and Headscale are trademarks of their respective owners.
Sphynx is an independent project and is not affiliated with or endorsed by
Tailscale Inc. or the Headscale authors.

### For developers

- Go 1.26 or newer: `go test ./...`
- Sphynx Chat's API tests drive the chat server with the web app's own
  encryption code: `cd internal/chat/test && npm ci && npm test`
- The chat's http encryption bundle is built from `internal/chat/webcrypto`:
  `npm ci && npm run build`
