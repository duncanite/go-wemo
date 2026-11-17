# go-wemo: the design

A Go module and one command, `wemo`, that speak everything a Wi-Fi Wemo plug speaks on
the LAN ([protocol.md](protocol.md)): SSDP to find it, SOAP over HTTP to read and drive it,
UPnP eventing to hear it change. Run from the plug's own network. No cloud, no broker, no
daemon: those are later layers on this library, decided once the protocol has been
exercised against the real plugs.

## Shape

The plug, the plugs as files on top of it, and the command. UPnP itself, descriptions,
actions, events and SSDP, is [go-upnp](https://github.com/forkcloser/go-upnp)'s.

- `wemo`: the plug. `Open` takes an address, a URL, a host with its port, or a bare host
  probed through the ports a Wemo drifts among after a reboot, and the HTTP client every
  request goes through, and gives a `Plug`, which has the plug's `ID`. The verbs a person
  uses, typed: state, on, off, toggle with a duration, name, signal strength, time sync,
  firmware version and the auto-update switch, reset, setup onto a network; and `Call`,
  the generic path to any action the device declares. `Recognize` and `Find` tell a plug
  on the network, through a `Browser` the caller lends. `Listen` opens the listener plugs
  send their events to, at the address the caller gives, and `Watch` tells how a plug
  stands and then what changes. It reads and writes no file and logs nothing.
- `wemo/wemotest`: a plug for tests, speaking what one first-generation Switch was seen
  to speak.
- `fleet`: plugs as their settings say, for a reconciler to wire. What is kept of a plug,
  by its serial, is what it takes: its name, read back and compared, and its auto-update
  switch, sent on every apply and never read, with the MAC for the reader. `Survey` reads
  every plug a program found; `Compare` sets what is kept against what answered and sorts
  a plug into changed, unreachable or unmanaged; `Apply` makes one plug match its settings
  and reads it back, since a plug says ok to a change it does not make; `Parse` and
  `Format` are the settings from and to YAML. Unreachable is never forgotten: finding
  plugs is best effort. The plug has no action to read the auto-update switch back, so a
  plan says what it will send rather than whether it holds. It reads and writes no file.
- `cmd/wemo` and `internal/cli`: the command, kong under primordium's process setup, logger
  and transport. It opens the host's SSDP, through go-upnp's `ssdp`, and lends it to the
  library.
- `internal/files`: the fleet's settings as files, for the command: one YAML file per
  plug in a directory, named by the plug's serial, which has to be a file's name and
  nothing else. `plan`, `apply` and `dump` are the fleet over them.

## Decisions

- **The device is the specification.** Every action and argument the tool knows is read
  from the plug's own service descriptions at run time; `describe` prints them. The typed
  verbs are conveniences over `call`, not a second truth. The descriptions are loose, the
  plug declares `GetSignalStrength`'s result as an input, so the caller checks names and
  returns whatever the answer holds rather than refusing on direction.
- **UPnP is a library's, and not this one's.** Descriptions, SOAP, eventing and SSDP are
  go-upnp's, shared with every other UPnP device's library: this module had its own, and
  what it knew of a Wemo was mixed with what is true of any device.
- **A plug is its UDN.** `Plug.ID` is the UDN the description gives, `uuid:Socket-1_0-`
  and the serial: the same whatever the plug's address and its port, which drifts. A plug
  says it on the network too, at the head of its advertisement's name, and `Recognize`
  reads it there: the same value before a description is read and after, so a plug is
  adopted by who it is and found wherever it is.
- **An advertisement is a claim, and nothing here proves it.** A plug has no credential:
  whatever answers at an address is taken for the plug it says it is. The network the
  plug sits on is the whole of its security, for finding it as for driving it.
- **Finding a plug needs a means this module does not have, and does not take.** SSDP's
  port is one to a host, and it belongs to the program, not to a library. `Recognize` is
  the knowledge, with no socket: an advertisement in, a plug's identity and where it is
  out. `Find` is the search, through a `Browser` the caller lends, in the standard
  library's types: go-upnp's `ssdp.Browser` satisfies it in a few lines, and so does
  anything else that browses.
- **The events' listener is this module's, at an address that is the caller's.** A plug
  delivers its events by connecting back, so something has to listen: `Listen` does, where
  it is told, and `Watch` is told the URL its plug reaches that listener at, this host's
  address on the plug's network. Neither is guessed: a host has several addresses, and
  only the program knows which a plug can reach. The command works its own out, from the
  search that found the plug or from the route to it.
- **A Watch ends, and says why.** It tells how the plug stands, then each change, and
  returns when its context ends, when the listener is closed, or when the subscription can
  no longer be trusted to have told everything. Nothing subscribes again by itself.
- **A target is a name, a host or a URL.** A friendly name resolves through discovery; a
  host, with an optional port, skips it and probes; a URL is taken as the description.
- **Nothing falls back silently.** A plug that sends no first event is an error with the
  reason, in the command, not a poll. A port that drifted is probed and said. A fault from
  the plug is the plug's code and text, whether it came with a 500 or, as a Wemo also
  does, inside a 200.
- **The dangerous verbs ship, behind a flag.** `reset` wants `--yes`; `firmware` reads the
  version and turns auto-update off, never uploads; `setup` reads the passphrase from the
  environment, never an argument, and encrypts it the way the Belkin app did: AES-128-CBC
  under an MD5 of the plug's own MAC and serial, the key the device publishes in its
  `MetaInfo`.
- **Rules through `call`.** The rules store is a SQLite file inside a zip, pushed and
  fetched by SOAP. Reachable, not modelled, until someone wants to edit it.
- **Tests after the plugs have spoken.** The first version is proven against the house's
  four plugs; a fake plug for the tests is then written against what was confirmed, so it
  fakes the device and not an assumption about it.

## Running it

```bash
wemo discover                       # every plug on this network
wemo describe <target>              # the device and every action it declares
wemo get <target>                   # on or off
wemo on|off|toggle <target>         # with --for 10m on on
wemo watch <target>                 # events until interrupted
wemo call <target> basicevent GetSignalStrength
wemo setup --ssid <name>            # on the plug's own access point, WEMO_PASSPHRASE set
wemo dump <dir>                     # a file for every plug that answers and has none
wemo plan <dir>                     # the files against the plugs
wemo apply <dir>                    # every plug that answers made to match its file
```

`LOG_LEVEL=debug` shows every request and answer.
