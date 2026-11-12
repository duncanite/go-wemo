# This file is the project's own.
# Add recipes leveraging provided `do` ready-made recipes, or create your own.
# The import must be kept: it mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
lint: do::lint::default do::lint::go::default
fix: do::fix::default do::fix::go::default
test: do::test::go::default
security: do::security::default do::security::go::default

# The command, built from this module at the commit checked out and run on
# the plugs' own network: `just wemo discover`, `just wemo on "<name>"`.

[doc('Build wemo and run it: `just wemo discover`.')]
wemo +args: build-wemo
    .build/wemo {{ args }}

build-wemo:
    mkdir -p .build
    go build -o .build/wemo ./cmd/wemo
