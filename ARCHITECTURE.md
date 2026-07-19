# AgentOS Architecture

## 1. Design Principles

1. **The agent is the user.** Every interface in the system is optimized for consumption by a language model: structured, textual, deterministic, low-latency. Pixels are a compatibility layer, never the primary path.
2. **Don't rebuild what already speaks agent.** POSIX — files, processes, pipes, exit codes, environment variables — is already the best agent interface ever shipped. AgentOS is a distribution and userspace contract on top of a stock Linux kernel, not a new kernel.
3. **Capabilities, not credentials.** Agents never hold raw API keys or blanket permissions. They hold scoped, revocable, budgeted capability tokens issued by a broker.
4. **Everything is auditable.** Every tool call, file write, network egress, and dollar spent lands in an append-only structured ledger. Observability is a kernel service, not an afterthought.
5. **Humans get a viewport, not a desktop.** The human interface is a read-mostly dashboard for observing, approving, and interrupting. It renders *from* the ledger; it is never in the agent's execution path.
6. **Open extension points.** Applications are CLIs + MCP servers packaged in a manifest. Anyone can publish one. This is the VS Code strategy: own the platform contract, let the ecosystem build the apps.

## 2. Why Not Just "Screenshots + Mouse"?

The numbers make the case:

- A screenshot round-trip through a vision model costs **2–5 seconds per step**; reading the same UI state from an accessibility tree or API costs **under 100 ms**.
- A GUI agent needs a *sequence* of human-shaped actions (open menu → click → type → click) where an API agent needs **one function call**.
- Every screenshot burns vision-token cost; structured output burns a fraction of that in text tokens.

That's the order-of-magnitude gap. But there's a nuance that shapes the architecture: the world's software and data are not all behind APIs. Agents still need the web (headless browser) and occasionally legacy GUI apps. So AgentOS treats GUI automation the way Linux treats Windows binaries — as a **compatibility layer** (think WINE), present but never the native path:

- **Native path:** CLI / MCP / HTTP API — used ~95% of the time.
- **Web path:** headless Chromium with structured DOM/accessibility access (CDP), not screenshots-first.
- **Legacy path:** a computer-use bridge (screenshot + synthetic input) for the long tail, explicitly marked as slow-path in the ledger.

## 3. System Components

### 3.1 Isolation substrate: per-agent microVMs

Each agent session runs in its own microVM (Firecracker or Cloud Hypervisor) with a dedicated kernel. Containers share the host kernel and are one escape away from every other tenant; microVMs isolate at the hardware-virtualization boundary and still boot in ~150 ms. This matches what E2B proved at scale. On developer laptops, a container profile is available for convenience, clearly labeled as weaker isolation.

Snapshot/restore and VM forking (à la Morph) are first-class: an agent can checkpoint before a risky operation and the supervisor can roll back — the OS-level equivalent of `git stash` for the whole machine state.

### 3.2 `agentd` — init and session supervisor

The PID-1 replacement for the agent world. Where systemd's job is to bring up daemons and a display manager, `agentd`'s job is to bring up *agent sessions*:

- Spawns a session from a **session spec**: which agent runtime (Claude Code, OpenClaw, a custom SDK loop), which packages are mounted, which capabilities are granted, which budgets apply.
- Enforces **resource budgets**: wall-clock, CPU/RAM (via cgroups in the guest), token spend, and dollar spend (via `capd`, below).
- Handles lifecycle: suspend (VM snapshot), resume, fork, kill, and post-mortem (ledger finalization).
- Exposes a control API so sessions can be driven by humans (dashboard), by schedules (cron-like triggers), or by other agents (with a capability grant).

### 3.3 `toolbus` — MCP as the driver model

In a human OS, hardware vendors ship drivers against a kernel driver API. In AgentOS, service vendors ship **MCP servers** against the toolbus:

- A system-wide registry of installed MCP servers with health checks, versioning, and lazy startup (servers spawn on first call, like socket activation).
- **Deferred tool loading**: agents see a searchable index of tool names, not thousands of schemas in-context. Schemas load on demand. This is critical — context is the scarcest resource in the system, the agent-OS analog of RAM.
- Uniform middleware: every call through the bus is authenticated against the session's capabilities, rate-limited, retried per policy, and written to the ledger.

CLIs need no bus — they're invoked directly through the shell, which is the point: the shell *is* an agent-native interface. The bus exists for stateful/remote/authenticated services.

### 3.4 `capd` — capability broker (secrets, permissions, spend)

The security core, and the piece most existing stacks get wrong (they paste raw API keys into env vars):

- Secrets live in the broker, never in the agent's environment or filesystem. The agent requests an action; `capd` either injects the credential into the outbound call at a proxy layer or mints a short-lived scoped token.
- **Grants are capability-shaped**, not user-shaped: `net.egress:api.stripe.com:readonly`, `fs.write:/workspace/**`, `spend:llm:$5/session`, `mcp:github:repo=acme/site:push`. Deny by default.
- Grants can require **human approval** at issue time or at call time (the dashboard surfaces an approve/deny prompt), and every exercise of a grant is ledgered.
- Budgets are enforced here: when the `$5` LLM budget is exhausted, further model calls fail with a structured error the agent can reason about (ask for more, or wind down gracefully).

### 3.5 `apkg` — the package manager

An "app" for an agent is not a windowed binary; it's a bundle of capabilities-to-act:

```toml
# apkg manifest (illustrative)
[package]
name = "github-toolkit"
version = "1.4.0"

[provides]
clis = ["gh"]
mcp_servers = ["github"]
skills = ["pr-review", "release-notes"]   # prompt-space instructions

[requires]
capabilities = ["net.egress:api.github.com", "secret:github-token"]

[verify]
healthcheck = "gh auth status"
```

- Distribution rides on **OCI images/layers** — no new transport, existing registries work.
- The manifest's `requires` block is the permission prompt: installing a package proposes grants; `capd` holds them; nothing runs with more than it declared. (This is Android's permission model applied to tools.)
- `skills` are first-class package contents: versioned prompt-space workflows, because for agents, *instructions are software*.

### 3.6 `ledger` — the audit log as a kernel service

Append-only, structured (JSONL/OTel), and unified: shell commands, tool calls, file diffs, egress, spend, model calls with token counts. It serves four masters:

1. **The human dashboard** renders live session views from it.
2. **Compliance** — the answer to "what did the agent do and why" is a query, not archaeology.
3. **The agent itself** — a session can query its own (or a sub-agent's) history: the agent-native `/proc` and `dmesg`.
4. **Evaluation** — ledgers are replayable traces for regression-testing agents.

### 3.7 Scheduler — model quota as the shared resource

Traditional OSes schedule CPU time. Here the contended resources are **provider rate limits, token budgets, and dollars**, shared across many concurrent sessions (this is AIOS's core insight, adopted). The scheduler:

- Multiplexes sessions over provider API quotas with priorities and fair-share.
- Backpressures low-priority background agents when interactive sessions are active.
- Treats context-window pressure as memory pressure: long-running sessions get summarization/compaction as a system service rather than each runtime reinventing it.

### 3.8 Human viewport

A web dashboard, deliberately boring: session list, live ledger tail, file-diff view, approval queue for capability escalations, budget meters, kill switch. It talks to `agentd`'s control API and reads the ledger. It is the *only* GUI in the system, and no agent ever looks at it.

## 4. What the Agent Experiences

A session boots in under a second into a shell with:

- `/workspace` — its mounted project files.
- `agentctl` — self-service CLI: query own budgets, request capability escalation, fork a sub-session, schedule a wake-up.
- `tool` — the toolbus CLI: `tool search stripe`, `tool call github.create_pr …`.
- A `MANIFEST.md` at `/etc/agentos/` — machine-readable description of everything available in this session: packages, tools, grants, budgets. The agent never has to guess what it can do; discoverability is a contract.

No display server. No window manager. Nothing rendered for eyes.

## 5. Failure Modes We're Designing Against

| Risk | Mitigation |
|---|---|
| Prompt injection via tool output steering the agent | Capability confinement means a hijacked agent still can't exceed its grants; egress allowlists bound exfiltration; ledger makes the attempt visible |
| Secret leakage into context/logs | Secrets never enter the agent's environment; proxy-side injection; ledger redaction |
| Runaway spend | Hard budgets in `capd`, enforced outside the agent's trust boundary |
| Sandbox escape | microVM isolation as the default, not containers |
| Vendor lock-in fear (the platform-adoption killer) | Every extension point is an open standard already in the wild: MCP, OCI, POSIX, OTel |
