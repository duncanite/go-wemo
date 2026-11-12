# go-wemo

The Wi-Fi Wemo plugs and switches, after Belkin: a Go library and command for what they
still speak on the LAN, which is everything a house needs from them. Discovery over SSDP,
control and state over the plug's SOAP service, state changes over UPnP eventing, and the
setup of a factory-reset plug onto a network without Belkin's app, which no longer exists.

Belkin ended the Wemo cloud on 2026-01-31 and let the domain behind the plugs' API and
firmware host lapse. A plug keeps trying to reach it, tens of thousands of times a day, and
whoever registers the name answers. The plugs belong behind a firewall that lets them reach
nothing outside, and this is the tool for talking to them inside.

The command, run from the plugs' own network:

```bash
just wemo discover                         # every plug that answers
just wemo get "Kitchen Lamp"               # by name, found by discovery
just wemo toggle 192.0.2.10                # by address, no discovery
just wemo watch 192.0.2.10                 # events until interrupted
just wemo call 192.0.2.10 basicevent GetSignalStrength
just wemo describe 192.0.2.10 -s basicevent   # every action the plug declares
```

[docs/design.md](docs/design.md) is the shape of the module and the decisions behind it;
[docs/protocol.md](docs/protocol.md) is what a plug speaks, on the LAN and toward the cloud,
with ports, hosts, sources, and what one plug answered.

## Verifying a release

Every archive, its SBOM and `checksums.txt` carry GitHub's build provenance attestation,
and `checksums.txt` a keyless signature by the release workflow at the tag, the one asset
that is a signature and not a subject; the one command covers an archive, its SBOM and the
checksums file alike:

```sh
gh attestation verify wemo_<version>_linux_amd64.tar.gz --repo duncanite/go-wemo
```

For a verifier with cosign and no `gh`, the signature over `checksums.txt`, then the
checksums themselves:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity-regexp '^https://github.com/duncanite/go-wemo/\.github/workflows/release\.yaml@refs/tags/v'
sha256sum -c checksums.txt
```

## Working on it

```bash
just lint      # the shared limen baseline and the Go lanes
just test      # the Go tests
just security  # the shared scans and the link check
just fix       # repair what `just lint` reports
```

`just --list` shows the rest.
