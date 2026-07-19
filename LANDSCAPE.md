# Landscape: Who Else Is Building This?

Short answer: the *layers* exist; the *distribution* doesn't. The space validates the thesis without owning it.

## 1. Sandbox / compute layer (well-funded, commoditizing)

| Project | What it is | Notes |
|---|---|---|
| **E2B** (YC-backed, ~$35M raised, open source) | Firecracker microVM sandboxes for agent code execution | The most battle-tested SDK in the space; proves per-agent microVMs at scale |
| **Daytona** (~$24M Series A) | Docker-container sandboxes, sub-90ms cold starts | Pivoted to agent infra in 2025; container isolation is weaker than microVM |
| **Morph** | VMs with ~250ms cold start **and full state forking/branching** | Snapshot-and-fork is the standout primitive |
| **Modal, Fly.io Sprites, Freestyle, Blaxel, Northflank** | Adjacent code-execution/compute plays | The layer is crowding fast |

**Takeaway:** these sell *compute with isolation*. None of them define what runs inside — no package format, no capability model, no agent-facing contract. They are to AgentOS what a VPS provider is to Ubuntu.

## 2. Kernel-metaphor research (academic, open source)

- **AIOS** (Rutgers `agiresearch`, open source): an "LLM agent operating system" — scheduler, context manager, memory/storage managers, tool manager, access manager; treats LLM inference as the CPU to be scheduled. Strong ideas (we adopt the quota-scheduler insight), research-grade execution.
- **AgenticOS workshops at ASPLOS 2026 and SOSP 2026**: the systems community is now formally defining OS primitives for agents — isolation models, scheduling, observability. The field is *forming right now*; the standards are unwritten.
- **AOHP** (Android Open Harness Project): AOSP-based harness treating agents as first-class OS actors — the mobile-flavored cousin of this idea.
- Assorted 2026 papers (LiteCUA — "computer as MCP server", LUMOS — accessibility-grounded agent layer) all converging on the same insight: give agents structure, not pixels.

## 3. Agent runtimes / harnesses (the "apps")

- **OpenClaw** (open source, 140k+ GitHub stars in months): a gateway on your machine giving an LLM shell, filesystem, browser, and community "skills," surfaced through messaging apps. Explosive proof of demand — and a perfect example of an agent runtime that *needs* an OS underneath it (its security story is exactly the raw-keys-in-env problem `capd` exists to fix).
- **Claude Code / Claude Cowork** (Anthropic): worth correcting the premise slightly — Cowork does **not** primarily screen-record; it runs agents in a lightweight Linux VM with file and shell access. The screenshot-and-mouse mode ("computer use") is the *fallback* for legacy GUI software. Anthropic already ships a proto-agent-OS inside a VM; they just don't sell the OS layer itself.
- **Agent S, UFO², operator-style GUI agents**: the pixel-path camp; the industry consensus is now hybrid (accessibility tree + vision), i.e., even GUI agents are fleeing pure screenshots.

## 4. The gap AgentOS targets

Nobody owns the **userspace contract**:

1. **Package format** for agent apps (CLI + MCP server + skills + declared capabilities) — no equivalent of `.deb`/APK exists.
2. **Capability broker** — scoped, budgeted, revocable grants instead of raw API keys in env vars. Every incident report in this ecosystem traces back to this gap.
3. **Unified ledger** — audit/observability as an OS service rather than per-framework logging.
4. **The registry** — the app-store/marketplace position that the VS Code analogy actually points at. MCP server registries exist but are unauthenticated lists, not a trust-and-permissions ecosystem.

The strategic read: the sandbox layer is commoditizing downward (price per VM-second), and the runtime layer is fragmenting upward (every lab ships its own harness). The durable position is the contract between them — exactly where Android sat between ARM hardware and mobile apps.

## Sources

- [AIOS: LLM Agent Operating System (arXiv)](https://arxiv.org/abs/2403.16971) · [GitHub](https://github.com/agiresearch/AIOS)
- [AgenticOS Workshop @ SOSP 2026](https://os-for-agent.github.io/) · [@ ASPLOS 2026](https://os-for-agent.github.io/asplos-2026.html)
- [E2B blog: YC companies building AI agents](https://e2b.dev/blog/yc-companies-ai-agents)
- [Northflank: Daytona vs E2B](https://northflank.com/blog/daytona-vs-e2b-ai-code-execution-sandboxes) · [ZenML: E2B vs Daytona](https://www.zenml.io/blog/e2b-vs-daytona) · [Morph comparisons](https://www.morphllm.com/comparisons/daytona-alternative)
- [Upstash: Best sandbox providers for AI agents 2026](https://upstash.com/blog/best-sandbox-providers-for-ai-agents)
- [API Agents vs. GUI Agents: Divergence and Convergence (arXiv)](https://arxiv.org/pdf/2503.11069)
- [Zylos Research: Computer Use and GUI Agents in 2026](https://zylos.ai/research/2026-02-08-computer-use-gui-agents/)
- [Marc Bara: Who Is Building the Agent-Native Operating System?](https://medium.com/@marc.bara.iniesta/who-is-building-the-agent-native-operating-system-c6bae5a5a3f5)
- [LiteCUA: Computer as MCP Server (arXiv)](https://arxiv.org/pdf/2505.18829)
