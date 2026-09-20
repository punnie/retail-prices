// Command retail is the CLI entrypoint for the retail-prices platform:
// migrate | profile | refresh | report | serve.
package main

import (
	"context"
	"fmt"
	"os"

	"retail-prices/internal/migrate"
	"retail-prices/internal/profile"
	"retail-prices/internal/refresh"
	"retail-prices/internal/report"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	ctx := context.Background()
	cmd, args := os.Args[1], os.Args[2:]

	var err error
	switch cmd {
	case "profile":
		err = profile.Run(ctx, args)
	case "migrate":
		err = migrate.Run(ctx, args)
	case "refresh":
		err = refresh.Run(ctx, args)
	case "report":
		err = report.Run(ctx, args)
	case "serve":
		err = fmt.Errorf("not implemented yet")
	default:
		usage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "retail %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: retail <command> [args]

commands:
  profile   analyse the raw products log and print a data-quality report
  migrate   apply derived-schema migrations
  refresh   rebuild the derived layer from the raw log
  report    render a price-change report
  serve     run the web app`)
}
