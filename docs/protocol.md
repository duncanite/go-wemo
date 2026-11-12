# What a Wemo plug speaks

Notes from reading the sources and one house's resolver, 2026-11-11. The plugs in question
are the Wi-Fi Wemo switches and plugs Belkin stopped supporting on 2026-01-31: the Switch,
the Insight, the Mini and their kin, all on an OpenWrt-derived firmware. The Thread models
and the HomeKit-capable ones are a different animal and are not covered here.

## The state of the cloud

- Belkin shut the Wemo cloud on 2026-01-31, by its own notice: the app stops working,
  remote access and the Alexa and Google integrations stop, no new setup through the app or
  HomeKit; devices set up with HomeKit before that date keep working through Apple Home,
  the Thread models unaffected ([Belkin, Wemo Support
  Ending](https://www.belkin.com/support-article/?articleNum=335419)).
- `xbcs.net`, the domain behind the API (`api.xbcs.net`) and the firmware host
  (`fw.xbcs.net`), is not registered: Verisign's RDAP answers 404 for it on 2026-11-11.
  Whoever registers it answers the plugs.
- `xwemo.com` and `wemo2.com` are registered until 2027-05-14 and 2027-10-03, `lswf.net`
  until 2026-12-10, per the same RDAP, which names no registrant. Nothing answers on them.
- A plug keeps trying. One plug on one house network asked its resolver for `nat.wemo2.com`
  32,751 times and `api.xbcs.net` 6,094 times in 24 hours, `heartbeat.lswf.net` 434 times.
- Whether the on-device scheduler survived is disputed. The Dibby Wemo Manager project
  asserts that rules are still stored but no longer fire, the scheduler having relied on a
  nudge from the cloud over a pinned TLS connection that cannot be faked locally; a report
  in pywemo's tracker has a first-generation Switch firing locally written rules on its own
  after the shutdown, once `TimeSync` set its clock and zone. Model and firmware may decide
  it. Either way a controller on the LAN that does the scheduling needs neither.

## On the LAN

Everything useful. No cloud in any of it.

| Protocol | Port | What for |
|---|---|---|
| SSDP | UDP 1900, multicast 239.255.255.250 | Discovery: periodic `NOTIFY` announcements, answers to `M-SEARCH`. Does not cross a router; a controller on another network needs the plug's address or a leg on its network. |
| HTTP, UPnP device description | TCP 49153 | `GET /setup.xml`: model, serial, MAC, firmware, the service list with its control and event URLs. The port drifts after reboots; pywemo probes 49153, 49152, 49154, 49151, then 49155 to 49159. |
| SOAP over HTTP | the same port | Control and state: `urn:Belkin:service:basicevent:1` at `/upnp/control/basicevent1`, `GetBinaryState` and `SetBinaryState`; an Insight adds power readings. Unauthenticated. |
| GENA, UPnP eventing | the same port, plus a callback port on the subscriber | A controller `SUBSCRIBE`s and the plug opens HTTP connections back to it to push state changes. Across a firewall that drops plug-to-controller connections, pywemo falls back to polling. |
| NTP | UDP 123 | `pool.ntp.org`, observed on the house resolver. Nothing a LAN controller uses depends on the plug's clock. |
| DHCP, ARP, DNS | | The usual. The plug asks for AAAA records too, observed. |

Not spoken: mDNS, on these models. Only the HomeKit-capable models advertise `_hap._tcp`.

### The maintenance verbs, also unauthenticated

The same local interface carries the verbs the Belkin app used, none of them behind any
authentication:

- `ReSetup`, on `basicevent`, drops the plug back into setup mode. pywemo's `reset` calls it,
  with flags for the data and the Wi-Fi, and warns it may not work on every device.
- `UpdateFirmware(NewFirmwareVersion, ReleaseDate, URL, Signature, DownloadStartTime,
  WithUnsignedImage)`, on the `firmwareupdate` service: the plug fetches the image from the
  URL. Images are signed and downloaded over plain HTTP; what `WithUnsignedImage` permits is
  not documented ([pywemo wiki, WeMo
  Firmware](https://github.com/pywemo/pywemo/wiki/WeMo-Firmware)).
- `setAutoFWUpdate` with `enableAutoUpdate=0`, on `basicevent` where the model has it,
  turns automatic firmware updates off; the wiki's example checks for the method before
  calling it ([pywemo wiki, WeMo Cloud](https://github.com/pywemo/pywemo/wiki/WeMo-Cloud)).

Anything that reaches the plug's port can flip it, reset its Wi-Fi, or hand it a firmware
image. The network the plug sits on is the whole of its security.

### Setting one up without Belkin

1. Factory-reset or `ReSetup`: the plug raises its own access point, named `WeMo.` and
   more, per pywemo's README.
2. Join it and discover the plug there, as on any network.
3. `setup(ssid, password)`: pywemo encrypts the passphrase with a key derived from the
   plug's own metadata, calls `ConnectHomeNetwork` on the setup service, then `CloseSetup`.
   WPA2 with AES only; the passphrase is ignored on an open network.

## Toward the cloud

What the plugs try, all of it dead or dangerous, and what a firewall has to drop. Ports are
the sources' where given and the protocol's standard port otherwise, marked.

| Protocol | Port | Host | What for |
|---|---|---|---|
| HTTPS | TCP 8443 | `api.xbcs.net`; later firmware also `deviceapis.xwemo.com` | State reports and commands, with HTTP basic authentication inside the TLS. |
| MQTT over TLS | TCP 8883, standard, not observed | an AWS IoT endpoint under `iot.us-east-1.amazonaws.com` | Newer firmware; one user counted 165,000 requests a day. |
| STUN | UDP 3478, standard, not observed | `nat.wemo2.com` | NAT traversal so the cloud could reach the plug. The house resolver saw the lookup every few seconds. |
| ICMP | | `heartbeat.xwemo.com` or `heartbeat.lswf.net`, and 8.8.8.8 | The internet check. Some models blink a red LED when it fails. |
| HTTP | | `fw.xbcs.net` | Firmware, signed, over plain HTTP. The unregistered name. |

Two behaviours worth knowing when fencing them in: a plug that receives no DNS server from
DHCP does not try the cloud at all, keeping only the ICMP check; and blocking everything
outbound costs the plug nothing a LAN controller uses.

## Sources

- [pywemo](https://github.com/pywemo/pywemo), the library Home Assistant uses: its README
  for setup and reset, `pywemo/ouimeaux_device/__init__.py` for the port probing, the wiki's
  [WeMo Cloud](https://github.com/pywemo/pywemo/wiki/WeMo-Cloud) for the hosts, the DNS
  behaviour and `setAutoFWUpdate`, and [WeMo
  Firmware](https://github.com/pywemo/pywemo/wiki/WeMo-Firmware) for `UpdateFirmware`.
- [Belkin, Wemo Support Ending](https://www.belkin.com/support-article/?articleNum=335419):
  the date and the scope of the shutdown, the device list.
- [Belkin WeMo Switch Communications Analysis](https://www.scip.ch/en/?labs.20160218=),
  scip AG, 2016: the API on TCP 8443 and its authentication.
- [Can Allowlists Capture the Variability of Home IoT Device Network Behavior?](https://www.blaseur.com/papers/eurosp24-firewalls.pdf),
  EuroS&P 2024: the move to `deviceapis.xwemo.com` after a firmware update.
- [Dibby Wemo Manager](https://github.com/K0rb3nD4ll4S/dibby-wemo-manager): the claim that
  the scheduler stopped and the connection is pinned; and [pywemo issue
  773](https://github.com/pywemo/pywemo/issues/773), the report of a Switch still firing
  its rules.
- [SmartThings community thread on the
  shutdown](https://community.smartthings.com/t/belkin-ending-support-for-most-of-its-wemo-smart-home-accessories-on-january-31-2026/302891):
  the AWS IoT endpoint and its request count.
- [Hacking the WeMo WiFi switch](https://gist.github.com/issackelly/3214468), 2012: the
  first reading of `setup.xml`.
- "Investigating in Isolation: WeMo", on `blog.pepperland.cloud`, at
  `/investigating-in-isolation-wemo/`: the STUN traffic and the heartbeat hosts, cited from
  search excerpts. The site answered nobody on 2026-11-11 and the Wayback Machine holds no
  copy, so the address is given as text, not a link.
- Verisign RDAP, queried 2026-11-11, for the registration state of the four domains.
- One house resolver's log, 2026-11-11, for the counts and the NTP and AAAA behaviour.
