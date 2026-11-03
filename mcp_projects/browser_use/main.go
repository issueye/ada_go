package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"browseruse/internal/app"
	"browseruse/internal/cli"
	"browseruse/internal/config"
	"browseruse/internal/mcp"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "cli":
			os.Exit(cli.Run(os.Args[2:]))
		case "mcp":
			os.Exit(mcp.Run(os.Args[2:]))
		case "run":
			os.Exit(cli.RunScript(os.Args[2:]))
		case "help", "-h", "--help":
			usage()
			return
		}
	}

	cfg := config.Load()
	fs := flag.NewFlagSet("browser-use", flag.ContinueOnError)
	serve := fs.Bool("serve", false, "start the GUI and the control server")
	noRestore := fs.Bool("no-restore", false, "do not restore the previous tabs")
	url := fs.String("url", "", "open this URL in the first tab")
	port := fs.Int("port", 0, "control server port (0 chooses one)")
	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	if *url == "" && fs.NArg() > 0 {
		*url = fs.Arg(0)
	}
	if *serve {
		// Background CLI/MCP launches must not clobber the user's session.
		cfg.Session.Restore = false
		cfg.Session.Save = false
	}
	if *noRestore {
		cfg.Session.Restore = false
	}
	cfg.Server.Port = *port
	if err := app.Run(cfg, *url); err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `browser-use — a desktop browser for AI agents

  browser-use [--url URL] [--port N] [--no-restore]
  browser-use cli status
  browser-use cli tabs list
  browser-use cli navigate --tab t1 https://example.com
  browser-use cli snapshot --tab t1
  browser-use cli click --tab t1 --ref e12
  browser-use cli type --tab t1 --ref e11 --text hello --submit
  browser-use cli wait --tab t1 --for url --value /results
  browser-use cli network list --tab t1 --url-contains /api/
  browser-use cli network get --id n12 --max-body 20000
  browser-use cli console list --tab t1 --level error
  browser-use cli control release
  browser-use mcp                # MCP stdio server for DSH
  browser-use mcp --http --port 8766   # Streamable HTTP MCP
  browser-use cli call <name> --args '{"...":1}'
  browser-use cli start [--port N]              # start/reuse the HTTP (--serve) instance
  browser-use cli call status --client th_123   # audit/lease attribution (default cli-<pid>)

The CLI starts the app if it is not running.`)
}
