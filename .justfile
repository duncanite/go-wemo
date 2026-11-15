# This file is the project's own.
# Add recipes leveraging provided `do` ready-made recipes, or create your own.
# The import must be kept: it mounts every shared limen task under `just do ...`.
import '.limen/just/main.just'

# The FIRST recipe defined here becomes `just`'s default.
lint: do::lint::default do::lint::go::default
fix: do::fix::default do::fix::go::default
test: do::test::go::unit do::test::go::race do::test::go::cover
security: do::security::default do::security::go::default

# The command, built from this module at the commit checked out and run on
# the plugs' own network: `just wemo discover`, `just wemo on "<name>"`.

# The recipes take their arguments positionally and hand them on as "$@", so
# one with a space in it, a device's name, reaches the command as one argument.
[doc('Build wemo and run it: `just wemo discover`.')]
[positional-arguments]
wemo +args: build-wemo
    .build/wemo "$@"

build-wemo:
    mkdir -p .build
    go build -o .build/wemo ./cmd/wemo
