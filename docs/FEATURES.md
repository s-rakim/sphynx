# Sphynx and Tailscale's features

This page goes through [Tailscale's documentation](https://tailscale.com/docs)
section by section and says, for each feature, whether it works with Sphynx
and how.

Your devices run the official Tailscale apps, so everything the apps do on
their own works exactly as Tailscale documents it. What differs is the
server: instead of Tailscale's service, Sphynx runs
[Headscale](https://github.com/juanfont/headscale) 0.29.6, the open-source
Tailscale control server, and adds its own pieces on top.

| Mark | Meaning |
|---|---|
| ✅ | Works as Tailscale documents it. |
| ✅ **tested** | Also checked end to end in Sphynx's automated test, with real Tailscale clients. |
| 🔁 | Sphynx does it a different way (explained in the row). |
| ❌ | Not available, with the reason. |
| — | About Tailscale the company (plans, billing); doesn't apply. |

Rows without "tested" rely on Headscale's support for the feature, or are
features of the Tailscale apps themselves, and haven't been tried in Sphynx's
own test setup.

## No limits

| | Sphynx |
|---|---|
| Devices | Unlimited. Your PC is the only limit; Headscale's authors expect hundreds of devices to be fine. |
| Users | Unlimited. |
| Price, account, plan | Free. No account with anyone and no plan. |

## What Sphynx adds

These aren't in Tailscale; see the [README](../README.md) for each.

| Feature | |
|---|---|
| Admin web page | ✅ **tested**. Devices, users, access rules, routes, DNS, keys, logs and settings, also on a phone. |
| Sphynx home | ✅ **tested**. The PC running Sphynx joins your network as an exit node and can share your home network, with nothing to set up. |
| Approve devices with your password | ✅ **tested**. A device that signs in without a key opens a Sphynx page where you approve it with the admin password, instead of typing a command on the server. |
| Wake-on-LAN | ✅ **tested**. Wake a computer at home from anywhere, sent by a device on the same home network. |
| Sphynx Chat | ✅ **tested**. A WhatsApp-style messenger where every device is its own contact, end-to-end encrypted, with calls on its HTTPS address. |
| Weak-signal mode | ✅. Turns on the Tailscale app options that help on bad mobile networks (offline peer list, client-side reachability, UDP lifetime probing, automatic exit node, packet size discovery), plus a per-device "TCP 443 only" mode for strict firewalls. |
| Sphynx Link | ✅. A way home without port forwarding or a VPN app, using Tailscale's Tailcat. Starting and stopping it is tested; a live link needs internet access, which the test setup doesn't have. |
| Public shares | ✅ **tested**. Sphynx's stand-in for Funnel (see below). |
| Radio mode | ✅ **tested** with pretend radios. Short text messages over Meshtastic (LoRa) or APRS radio reach Sphynx without internet: status, ping, wake a computer with a one-time code, and chats in Sphynx Chat where each radio station is a contact. |
| One-file backup | ✅. Settings, keys, the device database and the chat in one ZIP file. |

## Start

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Install Tailscale | ✅ **tested** | Official apps for Windows, macOS, iPhone and iPad, Android, Linux, Apple TV, NAS systems and Docker, pointed at your Sphynx server. See [Connect your devices](../README.md#4-connect-your-devices). |
| Quickstart, quick guides | 🔁 | The [README](../README.md) and the admin page's **Add device** page. Migration guides from OpenVPN and other VPNs apply as written: they use subnet routers and exit nodes. |
| Set up an identity provider | ✅ | Single sign-on with any OpenID Connect provider (Google, Microsoft Entra ID, Okta, Authentik, Keycloak and others) under **Settings → Single sign-on**. Sign-in methods that aren't OpenID Connect (GitHub accounts, Apple ID, Tailscale's passkeys) need a bridge such as Authentik or Keycloak. Without single sign-on, you approve devices with the admin password. |

## Manage access

### Access control

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Grants | ✅ **tested** | **Access controls** edits Tailscale's policy file. Sphynx adds a few rules of its own (shown on the page) for Sphynx home, the chat and strict-firewall devices. |
| ACLs | ✅ | |
| Application capabilities | ✅ **tested** | Taildrive's `tailscale.com/cap/drive` is in the default policy. Others work when the program that reads them runs on your network. |
| Groups | ✅ | Groups in the policy file. Groups from your identity provider can't be used in rules (a Headscale limitation), but can limit who may sign in. |
| Tags | ✅ | `tagOwners` in the policy; set tags on the **Devices** page or with a pre-tagged auth key. |
| Autogroups | ✅ partly | `autogroup:internet`, `member`, `tagged`, `self`, `nonroot` and `danger-all` work. The role autogroups (`admin`, `owner`, `it-admin` and so on), `autogroup:shared` and plan-based ones don't exist, because Sphynx has no admin roles and no sharing between networks. |
| Hosts | ✅ | |
| IP sets | ❌ | Not supported by Headscale. List the addresses in the rule instead. |
| Policy tests (`tests`, `sshTests`) | ✅ | **Check** runs them, and a policy whose tests fail isn't saved. |
| Edit the policy file | ✅ **tested** | In the admin page, with a **Check** button. |
| Visual policy editor | ❌ | Sphynx has the text editor only. |
| GitOps | 🔁 | There's no ready-made GitHub Action. A script can sign in to the admin page's API (`POST /api/login`) and save the policy (`PUT /api/policy`). |
| Just-in-time access | ❌ | Tailscale's own approval workflows. Edit the policy for temporary access instead. |
| Tailscale SSH | ✅ **tested** | SSH rules with `accept` and with `check`. For `check`, the person opens the link shown in the terminal and confirms with the admin password on Sphynx's "Confirm it's you" page (or with single sign-on when that's on). |
| Device posture | ❌ | Headscale doesn't support `postures` or `srcPosture`, and there are no posture integrations (CrowdStrike, Intune and so on). |

### Manage devices

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Add a device | ✅ **tested** | **Add device** shows the steps for each kind of device with your server address filled in, and makes auth keys. |
| Add a device with a QR code | ✅ | The QR code the Tailscale app shows opens Sphynx's sign-in page on your phone (how Apple TV joins). |
| Device approval | 🔁 **tested** | Without single sign-on, every new device needs the admin password on the sign-in page, or a key you made. With single sign-on, people add their own devices by signing in; there's no separate approval queue. |
| Rename, remove | ✅ | **Devices** page. |
| Key expiry | ✅ | Device sign-ins never expire by default; set a lifetime under **Settings**, or expire a device now on the **Devices** page. |
| Filter devices | ✅ | Search by name, owner, address or tag, and filter online or offline devices. |
| Export list of devices | 🔁 | `docker exec sphynx headscale nodes list -o json` |
| Device web interface (`tailscale web`) | ✅ | Runs on the device itself. |
| Secure node state storage | ✅ | A feature of the Tailscale apps. |
| Device posture management | ❌ | See above. |

### Manage users

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Invite users | 🔁 | Add the person on the **Users** page. Then make an auth key for their devices, approve their devices on the sign-in page with "Device belongs to" set to their name, or turn on single sign-on and allow their address. |
| User approval | 🔁 | With single sign-on, list the allowed people, domains or groups under **Settings → Single sign-on**. Without it, you approve each device anyway. |
| User roles | ❌ | The admin page has one admin password; nobody else can sign in to it. |
| Remove and offboard users | ✅ | Remove their devices, then the user, on the **Devices** and **Users** pages. |
| Suspend users | ❌ | Expire or remove their devices instead. |
| Admin console session timeout | 🔁 | An admin page sign-in lasts 30 days in that browser; **Sign out** ends it. |
| Fast user switching | ✅ | A feature of the Tailscale apps, for example to switch between your Sphynx and a Tailscale account. |
| Export list of users | 🔁 | `docker exec sphynx headscale users list -o json` |

### Tailnet Lock

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Tailnet Lock | ❌ | Not supported by Headscale. Sphynx runs on your own PC, so whoever controls the PC controls the network: keep it and your backups safe. |

## Route traffic

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Subnet routers | ✅ **tested** | Sphynx home shares your home network (**Settings → Sphynx home**). Any other device can share its networks, approved under **Exit nodes & routes** or automatically with `autoApprovers`. |
| Site-to-site networking | ✅ | Linux subnet routers with `--snat-subnet-routes=false`, as Tailscale describes. |
| 4via6 subnet routers | ✅ | The routes are approved like any other route. Not tried in Sphynx's tests. |
| Exit nodes | ✅ **tested** | Sphynx home is one from the start; any device can be one. |
| Automatic and suggested exit nodes | ✅ | Turned on by weak-signal mode's **Automatic exit node**. |
| Mullvad exit nodes | ❌ | A paid add-on sold by Tailscale. Any computer or rented server of your own can be an exit node instead. |
| App connectors | ❌ | Not supported by Headscale. Use a subnet router for the service's address ranges. |
| MagicDNS | ✅ **tested** | Names like `laptop` and `laptop.sphynx.internal` (the domain is set on the **DNS** page). |
| DNS servers, split DNS, search domains | ✅ | **DNS** page, including DNS-over-HTTPS addresses such as NextDNS or Control D, and a Pi-hole on your network. |
| Extra DNS records | ✅ **tested** | **DNS** page; Sphynx uses one itself for the chat's HTTPS address. |
| High availability | ✅ | Two subnet routers for the same network, or several exit nodes, take over from each other. |
| Route selection with `via` | ✅ | Grants with `via`. |
| Regional routing | ❌ | Overlapping routes fail over, but aren't picked by region. |
| Peer relays | ✅ | **Settings → Sphynx home → Peer relay UDP port** makes Sphynx home a relay; other devices can relay with `tailscale set --relay-server-port` and a grant. |
| DERP relays | 🔁 | Tailscale's free public relays, plus Sphynx's built-in relay (**Settings → Relays**). Other relay servers of your own can't be added in the admin page. |

## Set up servers

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Set up a server | ✅ **tested** | `tailscale up --login-server <your server>` on Linux, macOS or Windows. |
| Use tags | ✅ | |
| Install with cloud-init | ✅ | Add `--login-server` and an auth key to Tailscale's cloud-init example. |
| Auth keys | ✅ **tested** | **Keys** page: reusable, ephemeral, pre-tagged, with an expiry. |
| OAuth clients, workload identity federation | ❌ | Headscale has neither; use auth keys, or API keys for scripts. |
| Key expiry | ✅ | See Manage devices. |
| Tailscale SSH | ✅ **tested** | See Access control. |
| HTTPS certificates (`tailscale cert`) | ❌ | They come from Tailscale's `ts.net` domain. Sphynx gets its own certificate for your domain from Let's Encrypt; give other services a certificate for a name of your own. |
| Ephemeral nodes | ✅ | Ephemeral auth keys; offline ephemeral devices are removed after the time set in **Settings**. |
| Run unattended | ✅ | A setting in the Windows app. |
| Automatic client updates | ✅ | **Settings → Automatic Tailscale updates** sets the default for devices that haven't chosen. |

## Access and share services

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Taildrop | ✅ **tested** | Between your own devices, from the share menu or `tailscale file cp`. On by default (**Settings**). |
| Taildrive | ✅ **tested** | Allowed for your own devices in the default policy. |
| Share nodes with other networks | 🔁 | Sphynx is one network. Add the person as a user and write an access rule that only lets them reach the device you want to share. |
| Tailscale Services | ❌ | Not supported by Headscale yet. Reach the service on its device's name instead. |
| Endpoint collection | ❌ | Tailscale's list of services found on devices isn't collected. |

## Share a web server

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Tailscale Funnel | 🔁 **tested** | **Public shares** publishes a web service from any device at `https://<your server>/s/<name>/`. Web (HTTP) services only. |
| Tailscale Serve | ✅ partly, **tested** | `tailscale serve --http` and `--tcp` work, reached by the device's name. `--https` doesn't, because it needs a `ts.net` certificate. |

## Logging, streaming and events

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Configuration audit logs | 🔁 | The **Logs** page lists what Sphynx changed (settings, access rules, approved devices and more) next to Headscale's own log. |
| Network flow logs | ❌ | Not supported by Headscale. |
| Log streaming | ❌ | |
| SSH session recording | ❌ | Not supported by Headscale. |
| Client metrics | ✅ | `tailscale metrics` on each device. |
| Webhooks | ❌ | |
| Client logs sent to Tailscale | 🔁 | Turned off: your devices don't send their logs to Tailscale Inc. |

## Manage your organization

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Billing, plans, contact preferences | — | Sphynx is free and has no account. |
| Tailnet name | 🔁 | The MagicDNS domain on the **DNS** page (default `sphynx.internal`). |
| Domain ownership | — | |

## Reference

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Policy file syntax, ACL and grant examples | ✅ | Everything except the parts listed above (device posture, IP sets, some autogroups). |
| CLI | ✅ | The `tailscale` command on each device. On the PC, Headscale's own command works too: `docker exec sphynx headscale --help`. |
| API | 🔁 | Headscale's API at `<your server>/api/v1/`, with an API key from the **Keys** page (documentation at `<your server>/swagger`). Tailscale's own API isn't available. Change the access policy in the admin page, not through this API, or Sphynx will put its own version back. |
| Debug menu, messages, terminology, interoperability | ✅ | About the Tailscale apps, which are unchanged. |
| Versions | ✅ | Headscale 0.29.6 supports the last ten releases of the Tailscale apps. Keep the apps updated. |

## Integrations

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Containers and Docker | ✅ **tested** | The official `tailscale/tailscale` image with `TS_AUTHKEY` and `--login-server` (the **Add device** page has the command). |
| Cloud servers, virtual machines | ✅ | Install Tailscale and point it at your server. |
| NAS (Synology, QNAP, TrueNAS, Unraid) | ✅ | Their Tailscale packages run the normal client: use the package's login server option, or `tailscale up --login-server` on its command line. |
| Firewalls (pfSense, OPNsense) | ✅ | The same: the normal client, pointed at your server. |
| Serverless apps, remote dev environments | ✅ | Tailscale's userspace mode with an ephemeral auth key and `--login-server`. |
| GitHub Actions | ✅ | Install Tailscale in the job and run `tailscale up --login-server <your server> --auth-key <ephemeral key>`. Tailscale's own action is built around Tailscale's OAuth clients. |
| Kubernetes operator | ❌ | It needs Tailscale's API and OAuth clients. Running Tailscale as a sidecar with an auth key works. |
| Terraform and other tools that use Tailscale's API | ❌ | They talk to Tailscale's API, which Sphynx doesn't have. |
| Web servers using `ts.net` certificates (Caddy) | ❌ | See HTTPS certificates. |

## Solutions

Tailscale's solution guides (Apple TV, Pi-hole ad blocking, Windows Remote
Desktop, RustDesk, PiKVM, coding from an iPad, locking down a server and the
rest) combine the features above, and work the same way with Sphynx when the
features they use are available here.

## Tailscale's other products

| Tailscale docs | Sphynx | Notes |
|---|---|---|
| Tailscale PAM | ❌ | A separate commercial product. |
| Aperture | ❌ | A separate commercial product. Sphynx Chat covers messaging between your devices. |
