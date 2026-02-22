<!-- [Created by Claude: cc97cf1a-ffd6-4a53-b128-64a87ddab3a2] -->
# Universal Agent Startup Mechanism in Gas Town — Research

**Bead:** gt-izc7
**Date:** 2026-02-22
**Status:** Research complete, no code changes

---

## Executive Summary

Gas Town's startup mechanism is **partially universal** — the `RuntimeConfig`, `AgentPresetInfo`, and `StartupFallbackInfo` systems provide a provider-neutral abstraction layer. However, the **implementation** contains Claude-specific assumptions that cause Codex (and potentially other non-hook agents) to die before receiving their first assignment.

The four hypotheses from the assignment are **all confirmed**, with nuance.

---

## Hypothesis Verification

### 1. Startup readiness is tied to Claude-like prompt semantics — CONFIRMED

**Evidence:**

- `WaitForRuntimeReady` (`tmux.go:1722`) polls for `ReadyPromptPrefix` (e.g., `"❯ "` for Claude). Codex has **no prompt prefix** — falls back to blind `time.Sleep(3s)`.
- `WaitForCommand` (`tmux.go`) checks `SupportedShells` for shell→agent transition. Codex binary name `codex` is not in `SupportedShells`, so it returns immediately — nudges may be sent before TUI is ready.
- `AcceptBypassPermissionsWarning` runs for **all agents** during `session_manager.go:376` despite only Claude needing it (`EmitsPermissionWarning` is only checked in `sling_helpers.go` for existing agents, not fresh spawns). This wastes 1s for Codex.

**Preset comparison:**

| Field | Claude | Codex | Gemini | OpenCode |
|-------|--------|-------|--------|----------|
| `ReadyPromptPrefix` | `"❯ "` | `""` | `""` | `""` |
| `ReadyDelayMs` | 10000 | 3000 | 5000 | 8000 |
| `SupportsHooks` | yes | no | yes | yes |
| `EmitsPermissionWarning` | yes | no | no | no |

Only Claude has active polling. All others use blind sleeping — but with different delay tolerances.

### 2. Fresh polecats skip direct injection — CONFIRMED with nuance

**Evidence:**

- `sling.go:681` — fresh polecats skip `injectStartPrompt()` because `SessionManager.Start()` is expected to deliver the work context.
- For Claude: the CLI prompt arg carries the beacon, and the `SessionStart` hook runs `gt prime` automatically. This works reliably.
- For Codex: `PromptMode: "none"` means no CLI arg prompt. The beacon is sent via tmux nudge after a 3s sleep + 2s delay. If Codex TUI is not ready, the nudge is lost.

The design is intentional (not a bug in sling), but the deferred delivery mechanism is unreliable for non-hook agents.

### 3. Startup stack includes Claude-specific assumptions — CONFIRMED

**Evidence (session_manager.go startup sequence for fresh polecats):**

1. `WaitForCommand` → returns immediately for Codex (line 373)
2. `AcceptBypassPermissionsWarning` → wastes 1s for Codex (line 376)
3. `SleepForReadyDelay` → blind 3s sleep (line 379)
4. Beacon nudge sent (line 383-390)
5. 2s delay for `gt prime` (line 395) — assumes `gt prime` runs in <2s
6. Startup nudge sent (line 400)
7. `VerifySurvived` check — only checks tmux session existence, not agent process liveness (line 409)
8. **Redundant** `WaitForRuntimeReady` in `polecat_spawn.go:287` — another 3s sleep

Total blind window: ~9.3s where Codex can crash undetected. The verification at step 7 passes as long as the tmux session shell remains, even if the Codex binary has exited.

### 4. No prompt recorded → zombie handling fires — CONFIRMED

**Evidence chain:**

- If nudge is lost, Codex starts with no assignment awareness
- Daemon `checkPolecatHealth` (`daemon.go:1428`) periodically checks polecats
- Deacon stuck detection (`stuck.go`) pings sessions and escalates after 3 consecutive failures
- Warrant files show this pattern: furiosa warrant says "no bead assigned, state=zombie"
- Alpha dog warrant shows "Last activity only 12s after session creation" — consistent with an agent that started but never processed its prompt

---

## What Exists Today (Provider-Neutral Infrastructure)

### 1. AgentPresetInfo Registry (`config/agents.go`)

9 agent presets: `claude`, `gemini`, `codex`, `cursor`, `auggie`, `amp`, `opencode`, `copilot`, `pi`. Each preset declares its startup capabilities via structured fields — this IS the universal interface for describing agent behavior.

Key fields controlling startup: `PromptMode`, `SupportsHooks`, `HooksProvider`, `EmitsPermissionWarning`, `ReadyPromptPrefix`, `ReadyDelayMs`, `ProcessNames`, `ResumeStyle`.

### 2. RuntimeConfig (`config/types.go:374`)

Per-session runtime configuration resolved from preset + rig/town overrides. Provider-neutral structure with sub-configs for session, hooks, tmux, and instructions.

### 3. StartupFallbackInfo (`runtime/runtime.go:163`)

The fallback matrix that adapts startup behavior based on `Hooks` and `PromptMode`:

| Hooks | PromptMode | Beacon Delivery | gt prime | Work Instructions |
|-------|-----------|----------------|----------|-------------------|
| yes | arg | CLI prompt arg | Hook auto-runs | In CLI prompt |
| yes | none | Nudge | Hook auto-runs | Nudge |
| no | arg | CLI prompt arg | Agent runs manually | Delayed nudge |
| **no** | **none (Codex)** | **Nudge** | **Agent runs manually** | **Delayed nudge** |

### 4. Hook Registration System (`runtime/runtime.go:19`)

`RegisterHookInstaller()` — provider-agnostic registry. Currently registered: `claude`, `gemini`, `opencode`, `copilot`.

### 5. SessionConfig (`session/lifecycle.go`)

Unified `StartSession()` with boolean flags: `WaitForAgent`, `AcceptBypass`, `ReadyDelay`, `VerifySurvived`. Used by dog sessions; polecats have their own equivalent in `session_manager.go`.

---

## What Gaps Remain

### Gap 1: No Active Readiness Detection for Non-Claude Agents

Only Claude has `ReadyPromptPrefix` for active polling. All other agents use blind `time.Sleep()`. There is no universal "agent is ready to receive input" signal.

**Potential remedies in the code:**
- `ReadyPromptPrefix` could be set for Codex if its TUI has a detectable prompt string
- Sentinel file approach: agent writes a file after init; startup polls for it
- Process-level detection: check for specific process state via `ProcessNames` field (already exists but not used for readiness)

### Gap 2: AcceptBypassPermissionsWarning Runs Unconditionally for Fresh Spawns

`session_manager.go:376` calls it for all agents. The `EmitsPermissionWarning` guard only exists in `sling_helpers.go:397` for existing agents. Fresh spawn path should check the preset flag.

### Gap 3: Nudge Delivery Has No Acknowledgment

Nudges are fire-and-forget tmux `send-keys`. There is no mechanism to verify the agent received and processed the nudge. If the TUI is not ready, the keystrokes are lost silently.

**Potential remedies:**
- Retry nudge with pane content verification (check if prompt appeared in pane output)
- Use `gt prime` output as an acknowledgment signal (poll for its effects)
- For Codex specifically: deliver assignment via `AGENTS.md` file content (which Codex reads on startup) instead of tmux nudge

### Gap 4: VerifySurvived Only Checks tmux Session, Not Agent Process

`HasSession()` returns true even when the agent binary has crashed but the shell remains. The `IsAgentAlive()` function exists (used by zombie detection) but is not called during startup verification.

**Potential remedy:** Replace `HasSession()` with `IsAgentAlive()` in the startup verification step.

### Gap 5: Polecat session_manager.go Duplicates session/lifecycle.go

Dog sessions use the unified `StartSession()` with clean boolean flags. Polecat startup lives in its own `session_manager.go` with inline logic. This makes it harder to fix startup issues uniformly.

### Gap 6: No Guaranteed Post-Spawn Assignment Delivery for Non-Hook Agents

For hook agents (Claude, Gemini, OpenCode, Copilot), the `SessionStart` hook delivers `gt prime` reliably. For non-hook agents (Codex, Cursor, Auggie, Amp), assignment delivery depends entirely on the nudge timing window.

**Potential remedies found in the code:**
- `InstructionsFile` field (`AGENTS.md` for Codex): could embed assignment instructions in the file before spawn, so the agent discovers work on startup without needing a nudge
- `StartupNudgeDelayMs` could be increased (currently 2000ms for Codex)
- A retry loop with pane content verification could replace the single fire-and-forget nudge

---

## How Other Agent Types Handle Startup

| Agent | Hooks | Prompt | ReadyDelay | Notes |
|-------|-------|--------|-----------|-------|
| **Claude** | SessionStart hook | CLI arg | 10s + polling | Most robust: hook + active polling + prompt delivery |
| **Gemini** | SessionStart hook | none | 5s blind | Hook-based, no prompt; beacon via nudge |
| **Codex** | none | none | 3s blind | Most fragile: no hook, no prompt, short delay |
| **Cursor** | none | none | 5s blind | Same problems as Codex |
| **Auggie** | none | none | 5s blind | Same problems as Codex |
| **Amp** | none | none | 5s blind | Same problems as Codex |
| **OpenCode** | SessionStart hook | arg | 8s blind | Hook-based like Claude; longer delay compensates for slow init |
| **Copilot** | Informational hooks | none | 5s blind | Hooks are instructions-only, not executable |
| **Pi** | none | none | 5s blind | Same problems as Codex |

**Pattern:** Agents with `SupportsHooks=true` get reliable assignment delivery. Agents without hooks share the same fragility as Codex — they all depend on the blind-sleep + nudge mechanism.

---

## Potential Remedies (Found in Codebase)

1. **Guard `AcceptBypassPermissionsWarning` with `EmitsPermissionWarning`** in `session_manager.go:376` (fix exists in sling_helpers.go, needs porting to fresh spawn path)

2. **Add `ReadyPromptPrefix` for Codex** — if Codex TUI has a detectable ready indicator, use it for active polling instead of blind sleep

3. **Increase `ReadyDelayMs` for Codex** from 3000 to 5000+ (simple, reduces race window)

4. **Replace `HasSession` with `IsAgentAlive` in startup verification** — detect agent binary crash, not just tmux session existence

5. **Remove redundant `WaitForRuntimeReady`** in `polecat_spawn.go:287` — it adds another 3s of blind delay after `session_manager.Start()` already slept

6. **Embed assignment in `AGENTS.md` before spawn** — Codex reads `AGENTS.md` on startup; write the beacon content there so assignment delivery is filesystem-based (reliable) rather than keystroke-based (fragile)

7. **Add nudge delivery verification** — after sending nudge, poll pane content to confirm the text appeared; retry if not

8. **Unify polecat startup with `session/lifecycle.go`** — consolidate the two startup paths so fixes apply uniformly

---

## Prior Art

The existing investigation at `refinery/rig/throw_away/codex-startup-investigation.md` (bead gt-f0u8) covers the same Codex crash chain and proposes similar fixes. This research extends that work by analyzing the full agent matrix and identifying the universal patterns.
