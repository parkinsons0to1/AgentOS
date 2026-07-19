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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/parkinsons0to1/AgentOS/internal/capd"
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
  run -spec <file> [-secrets <file>] [-state <dir>]   run a session to completion
  version                                             print version`)
}

func runCmd(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	specPath := fs.String("spec", "", "session spec YAML (required)")
	secretsPath := fs.String("secrets", "", "secrets YAML held by the broker (never exposed to the session)")
	stateRoot := fs.String("state", defaultStateRoot(), "directory for session state and ledgers")
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

	res, err := session.Run(ctx, sess, secrets, *stateRoot)
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
