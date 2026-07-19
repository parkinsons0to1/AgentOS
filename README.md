# AgentOS

**An operating system distribution built for AI agents as the primary user — not humans.**

## The Thesis

Every mainstream OS assumes a human at the controls: pixels rendered for eyes, a mouse for hands, menus for memory. When an AI agent uses macOS or Windows today, it screenshots the screen, feeds pixels through a vision model, and emits synthetic mouse events — paying 2–5 seconds per step to do what a structured call could do in milliseconds. The agent is wearing a human costume to talk to a machine.

AgentOS inverts the assumption. The agent is the first-class user. Its "display" is structured data. Its "input devices" are shells, APIs, and tool calls. The human gets a viewport *into* the system — a dashboard for observing and approving — but the human interface is the peripheral, not the core.

## What AgentOS Is (and Isn't)

- **It is not a new kernel.** Linux already speaks the agent's native language (files, processes, pipes, sockets). Writing a kernel would be rebuilding the one layer that already works.
- **It is a distribution + userspace contract**, the way Android is to Linux: an opinionated stack that defines how agents boot, discover tools, get credentials, spend money, and get audited.
- **It is a platform others build on** — the VS Code play. The extension points are open standards that already won: MCP servers as the driver model, CLIs as applications, OCI images as the packaging substrate.

## The Stack at a Glance

```
┌─────────────────────────────────────────────────────┐
│  Human Viewport (web dashboard: observe / approve)  │
├─────────────────────────────────────────────────────┤
│  Agent Sessions (Claude Code, OpenClaw, custom…)    │
├─────────────────────────────────────────────────────┤
│  agentd        — init/session supervisor            │
│  toolbus       — MCP registry & routing (the "IPC") │
│  capd          — capability broker (secrets, spend) │
│  apkg          — package manager for agent apps     │
│  ledger        — structured audit log ("dmesg")     │
├─────────────────────────────────────────────────────┤
│  Stock Linux kernel, per-agent microVM isolation    │
└─────────────────────────────────────────────────────┘
```

## Documents

- [`ARCHITECTURE.md`](./ARCHITECTURE.md) — full system design: components, capability model, package format, scheduling.
- [`LANDSCAPE.md`](./LANDSCAPE.md) — prior art (AIOS, E2B, Daytona, Morph, OpenClaw, academic AgenticOS workshops) and where AgentOS is differentiated.
- [`ROADMAP.md`](./ROADMAP.md) — MVP scoping: what to build first and what to deliberately reuse.

## Status

**v0.1 in progress.** `agentd` (session supervisor), the `capd` egress gateway/secrets broker, the JSONL ledger, and the session manifest contract are implemented and tested in Go. See [`ROADMAP.md`](./ROADMAP.md) for what's done and what's next.

## Quick start

```bash
go build -o agentd ./cmd/agentd

cp examples/secrets.yaml.example secrets.yaml   # fill in real values
chmod 600 secrets.yaml

./agentd run -spec examples/session.yaml -secrets secrets.yaml
```

Inside the session the agent finds:

- `$AGENTOS_MANIFEST` — a markdown contract listing its grants, tools, and budgets
- `$AGENTOS_GATEWAY` — the egress gateway; `curl $AGENTOS_GATEWAY/egress/api.github.com/zen` reaches GitHub **with credentials injected by the broker** — the key never enters the session
- `HTTP(S)_PROXY` preset to the gateway, so even tools that know nothing about AgentOS get grant-checked egress
- MCP tools via the gateway: `curl $AGENTOS_GATEWAY/tool/<server>` lists tools, `curl -X POST $AGENTOS_GATEWAY/tool/<server>/<tool> -d '{...}'` calls one — servers are declared in the spec and spawned lazily
- a ledger at `~/.agentos/sessions/<id>/ledger.jsonl` recording everything

Install a package (CLIs + MCP servers + skills in one bundle) and use it:

```bash
./agentd pkg install examples/packages/greeter   # prints the permission review
# add `packages: [greeter]` to your session spec — its CLI lands on PATH,
# its MCP server becomes callable at $AGENTOS_GATEWAY/tool/greeter/<tool>
```

Watch and control sessions from the human viewport:

```bash
./agentd dash        # http://127.0.0.1:7420 — session list, ledger tail, kill switch
```

Run the tests with `go test ./...`.
