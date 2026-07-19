# Roadmap

Principle: **buy the commodity layers, build the contract.** Kernel, hypervisor, transport, and registries all exist. The MVP is the thinnest slice that demonstrates the userspace contract end-to-end.

## v0.1 — "It boots" (single machine, weeks not months)

A bootable agent environment you can demo:

- [x] **`agentd` prototype**: single Go binary (`cmd/agentd`). Reads a session spec (YAML), launches an agent runtime with a hardened (non-inherited) environment, enforces wall-clock and egress-request budgets, writes the ledger.
- [x] **`ledger` v0**: JSONL append-only file per session (`internal/ledger`); session lifecycle, every egress request/denial, and budget violations captured.
- [x] **`capd` v0**: secrets held by the broker (`internal/capd`); outbound traffic through a per-session egress gateway that checks grants and injects credentials upstream-side. Raw keys never appear in the session env. *This is the demo that sells the project.*
- [x] **`MANIFEST.md` contract**: machine-readable "what can I do here" file generated into every session, path exported as `$AGENTOS_MANIFEST`.
- [x] **HTTPS/CONNECT egress mode**: the gateway also acts as a standard HTTP proxy (`HTTP(S)_PROXY` is set in every session), so unmodified tools get grant-checked egress; plain-HTTP proxying injects credentials, CONNECT tunnels enforce grants only (opaque TLS).
- [x] **Base image (OCI)**: Alpine-based headless image (`Dockerfile`) with `agentd` as the entrypoint; built and smoke-tested in CI. No display stack whatsoever.
- [ ] **Firecracker rootfs build** for microVM isolation (the container image is the weaker-isolation dev profile).

## v0.2 — "It has apps"

- [x] **`toolbus` v0**: MCP servers declared in the session spec, spawned lazily on first use (stdio JSON-RPC), reached over the gateway's `/tool` HTTP surface with a `max_tool_calls` budget; every call lands in the ledger. Deferred schema loading falls out of the design: agents list tool names first and fetch schemas per server on demand.
- [x] **`apkg` manifest spec** (YAML) + installer: a package = CLIs + MCP servers + skills + declared egress capabilities. `agentd pkg install` prints the permission review; sessions reference packages by name and Apply merges their contents (grants deduped, MCP name collisions rejected). See `examples/packages/greeter`. OCI-artifact distribution still to come.
- [x] **Human viewport v0**: `agentd dash` — session list with live status, poll-based ledger tail, kill switch (SIGTERM via meta.json PID). Approval queue still to come.
- [x] **Approval queue**: sessions request ungranted egress via `POST $AGENTOS_GATEWAY/escalate` with a reason; pending requests surface in the viewport for approve/deny; approvals grant egress only (never credentials) and both sides of the exchange are ledgered (`capability.requested` / `capability.approved` / `capability.denied`).
- [x] Real packages under `packages/`: **github** (gh-api CLI), **stripe** (stripe-api CLI), **web** (fetch) — all gateway-routed with broker-injected credentials, each with a skill; repo-integrity test keeps every shipped package installable.
- [x] **OCI distribution**: `agentd pkg push/pull` speak the OCI registry v2 protocol directly (blob upload, manifests, ghcr-style bearer token exchange, digest verification, traversal-safe unpack). Works against ghcr.io/Docker Hub/registry:2; loopback registries use plain HTTP.

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

The first tenant is AgentOS development itself: run the coding agents that build this repo inside `agentd` sessions — real API keys, real spend, real tool calls — exactly the workload that exercises `capd` budgets and the ledger.
