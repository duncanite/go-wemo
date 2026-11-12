// wemo speaks to the Wemo plugs on this network; the command is internal/cli.
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/mycophonic/primordium/app"
	"github.com/mycophonic/primordium/app/shutdown"
	"github.com/mycophonic/primordium/network"

	"github.com/duncanite/go-wemo/internal/cli"
)

// requestTimeout bounds one request to a plug; a plug answers in well
// under a second on its own network, and a setup-mode plug in a few.
const requestTimeout = 15 * time.Second

func main() {
	app.Main(&app.Options{Name: "wemo"}, func(ctx context.Context) error {
		// The plugs speak plain HTTP; the transport is primordium's for
		// its dialer and its logging, TLS never comes into it.
		client := &http.Client{Transport: network.NewTransport(), Timeout: requestTimeout}

		if code := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, client); code != cli.ExitOK {
			return shutdown.ExitError(code)
		}

		return nil
	})
}
