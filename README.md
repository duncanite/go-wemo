# go-wemo

The Wi-Fi Wemo plugs and switches, after Belkin: a Go library and command for what they
still speak on the LAN, which is everything a house needs from them. Discovery over SSDP,
control and state over the plug's SOAP service, state changes over UPnP eventing, and the
setup of a factory-reset plug onto a network without Belkin's app, which no longer exists.

Belkin ended the Wemo cloud on 2026-01-31 and let the domain behind the plugs' API and
firmware host lapse. A plug keeps trying to reach it, tens of thousands of times a day, and
whoever registers the name answers. The plugs belong behind a firewall that lets them reach
nothing outside, and this is the tool for talking to them inside.

Nothing here yet but the reading: [docs/protocol.md](docs/protocol.md) is what a plug speaks,
on the LAN and toward the cloud, with ports, hosts and sources.

## Working on it

```bash
just lint      # the shared limen baseline
just security  # the shared scans and the link check
just fix       # repair what `just lint` reports
```

`just --list` shows the rest.
