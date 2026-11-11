# go-wemo: the design

A Go module and one command, `wemo`, that speak everything a Wi-Fi Wemo plug speaks on
the LAN ([protocol.md](protocol.md)): SSDP to find it, SOAP over HTTP to read and drive it,
UPnP eventing to hear it change. Run from the plug's own network. No cloud, no broker, no
daemon: those are later layers on this library, decided once the protocol has been
exercised against the real plugs.

## Shape

Three packages, one per protocol layer, and the plug on top of them.

- `ssdp`: `Search` sends an M-SEARCH and collects the answers until the wait is over;
  `Listen` joins the multicast group and hands over every NOTIFY, alive and bye. Both take
  the target address as an option, so a test or an odd network can point them anywhere.
  Standard library: a UDP socket and a header parser.
- `upnp`: the device description (`setup.xml`); the service descriptions, every action with its arguments and
  every state variable, read from the device itself; a SOAP caller that takes an action
  name and a map of arguments and returns every element of the answer; eventing, GENA:
  subscribe, renew, unsubscribe, a callback listener, and the property-set parser. Faults
  are typed errors carrying the device's own code and text.
- `wemo`: the plug. Discovery by SSDP or by address, with the port probe a Wemo needs since
  its port drifts after a reboot; the verbs a person uses, typed: state,
  on, off, toggle with a duration, name, signal strength, time sync, firmware version and
  the auto-update switch, reset, setup onto a network; and `Call`, the generic path to any
  action the device declares. Watching is a subscription and nothing else: when the
  callback never arrives, the error says so, and the caller decides.
- `cmd/wemo`: the command, kong under primordium's process setup, logger and transport.

## Decisions

- **The device is the specification.** Every action and argument the tool knows is read
  from the plug's own service descriptions at run time; `describe` prints them. The typed
  verbs are conveniences over `call`, not a second truth. The descriptions are loose, the
  plug declares `GetSignalStrength`'s result as an input, so the caller checks names and
  returns whatever the answer holds rather than refusing on direction.
- **Standard library for the protocols.** SSDP is one datagram and a parser, GENA is a
  handful of headers over `net/http`, and no Go library ships GENA at all. The one
  dependency the protocols would buy is a Go floor and a second opinion on sixty lines.
- **A target is a name, a host or a URL.** A friendly name resolves through discovery; a
  host, with an optional port, skips it and probes; a URL is taken as the description.
- **Nothing falls back silently.** A subscription that gets no events is an error with the
  reason, not a poll. A port that drifted is probed and said. A fault from the plug is the
  plug's code and text, whether it came with a 500 or, as a Wemo also does, inside a 200.
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
```

`LOG_LEVEL=debug` shows every request and answer.
