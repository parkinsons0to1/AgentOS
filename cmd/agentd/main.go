// agentd is the AgentOS session supervisor: it launches an agent runtime
// under a session spec, brokers its network access through capd, and records
// everything to the ledger.
//
// Usage:
//
//	agentd run -spec session.yaml [-secrets secrets.yaml] [-state ~/.agentos/sessions]
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/parkinsons0to1/AgentOS/internal/apkg"
	"github.com/parkinsons0to1/AgentOS/internal/capd"
	"github.com/parkinsons0to1/AgentOS/internal/dash"
	"github.com/parkinsons0to1/AgentOS/internal/session"
	"github.com/parkinsons0to1/AgentOS/internal/spec"
)

const version = "0.1.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(runCmd(os.Args[2:]))
	case "pkg":
		os.Exit(pkgCmd(os.Args[2:]))
	case "dash":
		os.Exit(dashCmd(os.Args[2:]))
	case "version":
		fmt.Println("agentd", version)
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `agentd — AgentOS session supervisor

commands:
  run -spec <file> [-secrets <file>] [-state <dir>] [-packages <dir>]
        run a session to completion
  pkg install <dir> [-store <dir>]    install a package into the store
  pkg list [-store <dir>]             list installed packages
  dash [-state <dir>] [-addr <host:port>]
        serve the human viewport (session list, ledger tail, kill switch)
  version                             print version`)
}

func dashCmd(args []string) int {
	fs := flag.NewFlagSet("dash", flag.ExitOnError)
	stateRoot := fs.String("state", defaultStateRoot(), "directory of session state and ledgers")
	addr := fs.String("addr", "127.0.0.1:7420", "listen address")
	fs.Parse(args)

	fmt.Fprintf(os.Stderr, "agentd dash: viewport on http://%s (state: %s)\n", *addr, *stateRoot)
	if err := http.ListenAndServe(*addr, dash.Handler(*stateRoot)); err != nil {
		fmt.Fprintln(os.Stderr, "agentd:", err)
		return 1
	}
	return 0
}

func pkgCmd(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("pkg "+sub, flag.ExitOnError)
	store := fs.String("store", defaultPkgRoot(), "package store directory")

	switch sub {
	case "install":
		if len(rest) < 1 || strings.HasPrefix(rest[0], "-") {
			fmt.Fprintln(os.Stderr, "agentd pkg install: package directory argument required")
			return 2
		}
		fs.Parse(rest[1:])
		p, err := apkg.Install(rest[0], *store)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentd:", err)
			return 1
		}
		fmt.Printf("installed %s %s -> %s\n", p.Name, p.Version, p.Dir)
		// The requires block is the permission prompt: show what referencing
		// this package will grant to a session.
		for _, g := range p.Requires.Egress {
			cred := "no credential"
			if g.Secret != "" {
				cred = "injects secret " + g.Secret
			}
			fmt.Printf("  grants egress: %s (%s)\n", g.Host, cred)
		}
		for _, m := range p.Provides.MCPServers {
			fmt.Printf("  provides mcp server: %s\n", m.Name)
		}
		for _, c := range p.Provides.CLIs {
			fmt.Printf("  provides cli: %s\n", c)
		}
		return 0

	case "list":
		fs.Parse(rest)
		pkgs, err := apkg.List(*store)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentd:", err)
			return 1
		}
		if len(pkgs) == 0 {
			fmt.Println("no packages installed")
			return 0
		}
		for _, p := range pkgs {
			fmt.Printf("%s %s — %s\n", p.Name, p.Version, p.Description)
		}
		return 0

	default:
		usage()
		return 2
	}
}

func defaultPkgRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agentos/packages"
	}
	return filepath.Join(home, ".agentos", "packages")
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	specPath := fs.String("spec", "", "session spec YAML (required)")
	secretsPath := fs.String("secrets", "", "secrets YAML held by the broker (never exposed to the session)")
	stateRoot := fs.String("state", defaultStateRoot(), "directory for session state and ledgers")
	pkgRoot := fs.String("packages", defaultPkgRoot(), "package store directory")
	fs.Parse(args)

	if *specPath == "" {
		fmt.Fprintln(os.Stderr, "agentd run: -spec is required")
		return 2
	}

	sess, err := spec.Load(*specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentd:", err)
		return 1
	}

	secrets := capd.NewSecrets(nil)
	if *secretsPath != "" {
		secrets, err = capd.LoadSecrets(*secretsPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "agentd:", err)
			return 1
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := session.Run(ctx, sess, secrets, *stateRoot, *pkgRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentd:", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "agentd: session %s finished exit=%d timed_out=%v duration=%s\n",
		res.SessionID, res.ExitCode, res.TimedOut, res.Duration.Round(1e6))
	fmt.Fprintf(os.Stderr, "agentd: ledger at %s\n", filepath.Join(res.Dir, "ledger.jsonl"))
	if res.TimedOut {
		return 124
	}
	return res.ExitCode
}

func defaultStateRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".agentos/sessions"
	}
	return filepath.Join(home, ".agentos", "sessions")
}
