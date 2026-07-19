# Roadmap

Principle: **buy the commodity layers, build the contract.** Kernel, hypervisor, transport, and registries all exist. The MVP is the thinnest slice that demonstrates the userspace contract end-to-end.

## v0.1 — "It boots" (single machine, weeks not months)

A bootable agent environment you can demo:

- [ ] **Base image**: minimal Linux (Alpine or NixOS for reproducibility) as an OCI image + a Firecracker rootfs build. No display stack whatsoever.
- [ ] **`agentd` prototype**: single Go/Rust binary. Reads a session spec (YAML), launches one agent runtime (start with Claude Code headless / Agent SDK), enforces wall-clock and dollar budget, writes the ledger.
- [ ] **`ledger` v0**: JSONL append-only file per session; every shell command and MCP call captured (wrap execution, don't instrument runtimes yet).
- [ ] **`capd` v0**: secrets held by a local broker process; outbound HTTPS through a proxy that injects credentials per egress-allowlist rules. Raw keys never appear in the session env. *This is the demo that sells the project.*
- [ ] **`MANIFEST.md` contract**: the machine-readable "what can I do here" file mounted into every session.

## v0.2 — "It has apps"

- [ ] **`apkg` manifest spec** (TOML) + installer: a package = CLIs + MCP servers + skills + declared capabilities, shipped as an OCI artifact.
- [ ] **`toolbus` v0**: MCP server registry with lazy startup and deferred schema loading; all calls pass capability checks and land in the ledger.
- [ ] Port 3–5 real packages (github-toolkit, browser, stripe, postgres) to prove the format.
- [ ] **Human viewport v0**: local web dashboard — session list, live ledger tail, approval queue, kill switch.

## v0.3 — "It's a platform"

- [ ] Session **snapshot/fork/rollback** (Firecracker snapshots; evaluate Morph-style branching).
- [ ] **Scheduler**: fair-share multiplexing of provider quotas across concurrent sessions.
- [ ] **Registry** (the marketplace seed): publish/install `apkg` packages with signatures and a permissions review surface.
- [ ] Multi-runtime support: OpenClaw and a bare Agent SDK loop running side by side under `agentd`, proving runtime-neutrality.

## Explicit non-goals (for now)

- Writing a kernel, hypervisor, or container runtime.
- A GUI environment for agents — pixels stay a compatibility layer.
- Competing with E2B/Daytona on hosted compute. AgentOS should *run on* them (their VMs are hardware; we're the distro), not against them.
- Inventing new protocols where MCP/OCI/OTel already have gravity.

## Dogfooding path

Warden's `growth_agents` (content/marketing automation in WardenDesktopApp) is the first tenant: real agents, real API keys, real spend — exactly the workload that exercises `capd` budgets and the ledger. Ship the OS by running the company's own agents on it.
