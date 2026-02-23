<!-- [Created by Claude: cc97cf1a-ffd6-4a53-b128-64a87ddab3a2] -->
<!-- [Edited by Claude: f2fe8a96-315e-479b-b94a-f4e8fe885eed] -->
# Universal Agent Startup Mechanism in Gas Town — Research

**Bead:** gt-izc7
**Date:** 2026-02-23
**Status:** Research complete, no code changes

---

## Executive Summary

Gas Town's startup mechanism is **partially universal** — the `AgentPresetInfo` registry, `RuntimeConfig`, and `StartupFallbackInfo` systems provide a provider-neutral abstraction layer. However, the **implementation** contains Claude-specific assumptions that cause Codex (and potentially other non-hook agents) to die before receiving their first assignment.

The four hypotheses from the assignment are **all confirmed**, with detailed evidence below.

The codebase already contains the building blocks for a truly universal startup interface — the gap is in the execution paths, not the data model.

---

## Hypothesis Verification

### 1. Startup readiness is tied to Claude-like prompt semantics — CONFIRMED

**Evidence:**

- `WaitForRuntimeReady` (`tmux.go:1681-1757`) has two paths:
  - **Active polling** (Claude only): polls tmux pane every 200ms for `ReadyPromptPrefix` (e.g., `">"`) using `matchesPromptPrefix()` (line 1709). Normalizes NBSP (U+00A0) to regular space.
  - **Blind sleep fallback** (all other agents): `time.Sleep(ReadyDelayMs)` (line 1732). No verification.
- `WaitForCommand` (`tmux.go`) checks `SupportedShells` for shell→agent transition. Codex binary name `codex` is not in `SupportedShells`, so it returns immediately — nudges may be sent before TUI is ready.
- `AcceptBypassPermissionsWarning` runs for **all agents** during `session_manager.go:376` despite only Claude needing it (`EmitsPermissionWarning` is only checked in `sling_helpers.go:880-891` for existing agents, not fresh spawns). This wastes 1s for Codex.

**Complete preset comparison:**

| Field | Claude | Codex | Gemini | OpenCode | Copilot | Cursor | Auggie | AMP | Pi |
|-------|--------|-------|--------|----------|---------|--------|--------|-----|-----|
| `ReadyPromptPrefix` | `">"` | `""` | `""` | `""` | `">"` | `""` | `""` | `""` | `""` |
| `ReadyDelayMs` | 10000 | 3000 | 5000 | 8000 | 5000 | — | — | — | — |
| `SupportsHooks` | yes | **no** | yes | yes | **informational** | **no** | **no** | **no** | yes |
| `PromptMode` | arg | **none** | arg | arg | arg | arg | arg | arg | — |
| `EmitsPermissionWarning` | yes | no | no | no | no | no | no | no | no |

Only Claude has active polling. Copilot has `ReadyPromptPrefix` but its hooks are informational-only.

### 2. Fresh polecats skip direct injection — CONFIRMED with nuance

**Evidence:**

- `sling.go:681` — fresh polecats skip `injectStartPrompt()` because `SessionManager.Start()` is expected to deliver the work context.
- For Claude: the CLI prompt arg carries the beacon, and the `SessionStart` hook runs `gt prime --hook && gt mail check --inject` automatically. This works reliably.
- For Codex: `PromptMode: "none"` means no CLI arg prompt. The beacon is sent via tmux nudge after a 3s sleep + 2s delay. If Codex TUI is not ready, the nudge keystrokes are lost.

The design is intentional (not a bug in sling), but the deferred delivery mechanism is unreliable for non-hook agents.

### 3. Startup stack includes Claude-specific assumptions — CONFIRMED

**Evidence (session_manager.go startup sequence for fresh polecats, lines 190-433):**

```
Step  | Action                              | Time Cost  | Claude-specific?
------|-------------------------------------|-----------|------------------
1     | WaitForCommand → shell→agent        | ~instant  | Returns immediately for Codex
2     | AcceptBypassPermissionsWarning      | 1s sleep  | YES — only Claude emits this
3     | SleepForReadyDelay                  | 3s (codex)| Uses preset value but no verification
4     | Beacon nudge sent                   | ~0s       | Fire-and-forget
5     | Sleep for StartupNudgeDelayMs       | 2s        | Assumes gt prime runs in <2s
6     | Startup nudge sent                  | ~0s       | Fire-and-forget
7     | VerifySurvived → HasSession()       | ~0s       | Only checks tmux session, NOT agent
8     | WaitForRuntimeReady (polecat_spawn) | 3s (codex)| REDUNDANT — second blind sleep
```

Total blind window: **~9.3s** where Codex can crash undetected. The verification at step 7 passes as long as the tmux session shell remains, even if the Codex binary has exited.

**Unconditional AcceptBypassPermissionsWarning callers** (waste 1s for non-Claude):
- `session_manager.go:376` (fresh polecat spawn)
- `daemon/lifecycle.go:397` (daemon restart)
- `daemon/daemon.go:1645` (daemon restart)
- `deacon/manager.go:161` (deacon startup)
- `refinery/manager.go:199` (refinery startup)
- `witness/manager.go:206` (witness startup)
- `dog/session_manager.go:123` (via `AcceptBypass: true`)

**Only guarded caller:** `sling_helpers.go:880-891` uses `shouldAcceptPermissionWarning()` which checks `preset.EmitsPermissionWarning`.

### 4. No prompt recorded → zombie handling fires — CONFIRMED

**Evidence chain:**

1. If nudge is lost, Codex starts with no assignment awareness
2. Daemon `checkPolecatHealth` (`daemon.go:1430`) periodically checks polecats
3. If `agent_state="spawning"` but updated >5 minutes ago: spawning guard expires
4. If session is dead AND polecat has `hook_bead` set: "CRASH DETECTED" (daemon.go:1500)
5. Deacon stuck detection (`stuck.go`) pings sessions and escalates after 3 consecutive failures
6. Warrant files confirm: "no bead assigned, state=zombie" and "Last activity only 12s after session creation"

---

## What Exists Today (The Universal Interface)

### 1. AgentPresetInfo Registry — The Single Source of Truth

**File:** `internal/config/agents.go:42-132`

```go
type AgentPresetInfo struct {
    Name                   AgentPreset
    Command                string
    Args                   []string
    Env                    map[string]string
    ProcessNames           []string
    SessionIDEnv           string
    ResumeFlag             string
    ContinueFlag           string
    ResumeStyle            string              // "flag" or "subcommand"
    SupportsHooks          bool
    SupportsForkSession    bool
    NonInteractive         *NonInteractiveConfig
    PromptMode             string              // "arg" or "none"
    ConfigDirEnv           string
    ConfigDir              string
    HooksProvider          string
    HooksDir               string
    HooksSettingsFile      string
    HooksInformational     bool
    ReadyPromptPrefix      string
    ReadyDelayMs           int
    InstructionsFile       string
    EmitsPermissionWarning bool
}
```

9 built-in presets registered (lines 161-343): `claude`, `gemini`, `codex`, `cursor`, `auggie`, `amp`, `opencode`, `copilot`, `pi`.

Extensible via `settings/agents.json` at town or rig level (loaded by `LoadAgentRegistry()`). No code changes required for new agents.

### 2. RuntimeConfig — Per-Session Runtime Configuration

**File:** `internal/config/types.go:374-467`

```go
type RuntimeConfig struct {
    Provider      string
    Command       string
    Args          []string
    Env           map[string]string
    InitialPrompt string
    PromptMode    string                    // "arg" or "none"
    Session       *RuntimeSessionConfig     // SessionIDEnv, ConfigDirEnv
    Hooks         *RuntimeHooksConfig       // Provider, Dir, SettingsFile, Informational
    Tmux          *RuntimeTmuxConfig        // ProcessNames, ReadyPromptPrefix, ReadyDelayMs
    Instructions  *RuntimeInstructionsConfig // File (CLAUDE.md / AGENTS.md)
    ResolvedAgent string                    // runtime-only, not serialized
}
```

**Resolution chain** (`loader.go:1108`):
1. `GT_COST_TIER` env var (ephemeral tier override)
2. Rig's `RoleAgents[role]` (per-role override)
3. Town's `RoleAgents[role]` (per-role override)
4. Rig's `Agent` field
5. Town's `DefaultAgent`
6. Ultimate fallback: `"claude"`

`normalizeRuntimeConfig()` (types.go:535) fills empty fields from the preset via ~11 `default*()` functions.

### 3. StartupFallbackInfo — The Readiness Decision Matrix

**File:** `internal/runtime/runtime.go:163-228`

```go
type StartupFallbackInfo struct {
    IncludePrimeInBeacon bool   // beacon should say "Run gt prime"
    SendBeaconNudge      bool   // beacon must be sent via tmux nudge (no prompt support)
    SendStartupNudge     bool   // work instructions need separate nudge
    StartupNudgeDelayMs  int    // wait for gt prime before sending work
}
```

**Fallback matrix:**

| Hooks | Prompt | Beacon Delivery | Context Source | Work Instructions |
|-------|--------|----------------|----------------|-------------------|
| yes | arg | CLI prompt arg | Hook runs `gt prime` | In beacon |
| yes | none | Nudge | Hook runs `gt prime` | Same nudge (delay=0) |
| no | arg | CLI prompt + "Run gt prime" | Agent runs manually | Delayed nudge (2s) |
| **no** | **none (Codex)** | **Nudge + "Run gt prime"** | **Agent runs manually** | **Delayed nudge (2s)** |

### 4. Hook Registration System

**File:** `internal/runtime/runtime.go:19-37`

Four providers registered via `RegisterHookInstaller()`:

| Provider | Hook Type | Implementation |
|----------|-----------|----------------|
| `claude` | `settings.json` lifecycle hooks | `claude.EnsureSettingsForRoleAt()` — SessionStart, PreCompact, UserPromptSubmit, PreToolUse, Stop |
| `gemini` | `settings.json` (different event names) | `gemini.EnsureSettingsForRoleAt()` — SessionStart, PreCompress, BeforeAgent, BeforeTool, SessionEnd |
| `opencode` | JS plugin | `opencode.EnsurePluginAt()` — session.created, session.compacted events |
| `copilot` | Markdown instructions (informational) | `copilot.EnsureSettingsAt()` — NOT executable hooks |

Agents without hook installers (Codex, Cursor, Auggie, AMP) rely entirely on the nudge-based fallback.

### 5. Beacon System — Session Identification

**File:** `internal/session/startup.go`

```go
type BeaconConfig struct {
    Recipient               string  // "polecat rust (rig: gastown)"
    Sender                  string  // "witness", "deacon"
    Topic                   string  // "assigned", "cold-start", "handoff", "patrol"
    MolID                   string
    IncludePrimeInstruction bool    // non-hook agents: "Run gt prime"
    ExcludeWorkInstructions bool    // non-hook: work comes as delayed nudge
}
```

Format: `[GAS TOWN] <recipient> <- <sender> • <timestamp> • <topic[:mol-id]>`

### 6. Wrapper Scripts (Tier 3 Deep Integration)

**File:** `internal/wrappers/scripts/gt-codex`, `gt-gemini`, `gt-opencode`

All three follow the same pattern:
```bash
if gastown_enabled && command -v gt &>/dev/null; then
    gt prime 2>/dev/null || true
fi
exec <agent> "$@"
```

These run `gt prime` before launching the agent binary — a filesystem-level backup for agents that lack native hooks.

### 7. Unified StartSession (for non-polecat roles)

**File:** `internal/session/lifecycle.go:15-255`

`StartSession()` uses clean boolean flags: `WaitForAgent`, `AcceptBypass`, `ReadyDelay`, `AutoRespawn`, `VerifySurvived`. Used by dog, crew, mayor, deacon, witness, refinery, boot. Polecats have their own parallel path in `session_manager.go`.

---

## The Complete Spawn-to-Work Flow

### Claude Polecat (hooks=yes, prompt=arg)

```
gt sling gt-abc myrig
  ├── SpawnPolecatForSling() → allocate name, create worktree
  ├── hookBeadWithRetry() → bead status=hooked
  ├── storeFieldsInBead() → dispatcher, args, attached_molecule
  ├── CreateDoltBranch() → flush + fork
  └── StartSession()
      └── SessionManager.Start()
          ├── ResolveRoleAgentConfig("polecat") → claude preset
          ├── EnsureSettingsForRole() → install SessionStart hook
          ├── GetStartupFallbackInfo() → {no fallbacks needed}
          ├── BuildPolecatStartupCommand() → "exec env ... claude --dangerously-skip-permissions '<beacon>'"
          ├── NewSessionWithCommand() → tmux session with beacon as CLI prompt
          ├── WaitForCommand() → wait for shell→node/claude
          ├── AcceptBypassPermissionsWarning() → dismiss dialog ✓
          ├── SleepForReadyDelay(10s) → wait for Claude
          └── VerifySurvived() → HasSession()

      Claude starts → reads beacon → SessionStart hook fires →
      gt prime --hook → loads context + hooked work → BEGINS IMMEDIATELY
```

### Codex Polecat (hooks=no, prompt=none)

```
gt sling gt-abc myrig --agent codex
  ├── SpawnPolecatForSling() → allocate name, create worktree
  ├── hookBeadWithRetry() → bead status=hooked
  ├── storeFieldsInBead()
  ├── CreateDoltBranch()
  └── StartSession()
      └── SessionManager.Start()
          ├── ResolveRoleAgentConfig("polecat") → codex preset
          ├── EnsureSettingsForRole() → NO hooks installed
          ├── GetStartupFallbackInfo() → {IncludePrime:T, BeaconNudge:T, StartupNudge:T, Delay:2000}
          ├── BuildPolecatStartupCommand() → "exec env GT_AGENT=codex ... codex --dangerously-bypass-..."
          │   (NO beacon as prompt — PromptMode="none")
          ├── NewSessionWithCommand() → tmux session
          ├── WaitForCommand() → INSTANT (codex not in SupportedShells)
          ├── AcceptBypassPermissionsWarning() → WASTES 1s (codex doesn't emit this)
          ├── SleepForReadyDelay(3s) → blind sleep
          ├── NudgeSession(beacon) → "[GAS TOWN] ... Run gt prime"  ← FIRE AND FORGET
          ├── Sleep(2s) → hope gt prime finishes
          ├── NudgeSession("Check your hook...") ← FIRE AND FORGET
          ├── RunStartupFallback() → NudgeSession("gt prime && gt mail check --inject")
          ├── VerifySurvived() → HasSession() (NOT IsAgentAlive!)
          └── WaitForRuntimeReady(3s) → ANOTHER blind sleep (polecat_spawn.go:287)

      If Codex TUI ready in time → receives nudge → runs gt prime → sees hook → WORKS
      If Codex TUI NOT ready → keystrokes lost → no assignment → ZOMBIE
```

---

## How Other Agent Types Handle Startup

### Agents with Native Hooks (Reliable)

| Agent | Hook Mechanism | Startup Behavior |
|-------|---------------|-----------------|
| **Claude** | `settings.json` lifecycle hooks | SessionStart → `gt prime --hook && gt mail check --inject`. Most robust: hook + active polling + prompt delivery. |
| **Gemini** | `settings.json` (different event names) | SessionStart → `gt prime --hook`. Prompt via CLI arg. No active polling but longer 5s delay compensates. |
| **OpenCode** | JS plugin (`gastown.js`) | `session.created` → runs `gt prime` via shell subprocess. Plugin also handles `session.compacted`. Prompt via CLI arg. 8s delay. |
| **Pi** | JS extension (`gastown-hooks.js`) | Similar to OpenCode's plugin pattern. |

### Agents with Informational Hooks (Partial)

| Agent | Hook Mechanism | Startup Behavior |
|-------|---------------|-----------------|
| **Copilot** | Markdown instructions | `HooksInformational: true` — hooks are instructions-only, not executable. Treated as "no real hooks" by the fallback matrix. Has `ReadyPromptPrefix: ">"` for active polling. Gets delayed nudge. |

### Agents without Hooks (Fragile)

| Agent | PromptMode | Startup Behavior |
|-------|-----------|-----------------|
| **Codex** | none | Most fragile: no hook, no prompt, 3s blind delay. Beacon + work via two nudges. |
| **Cursor** | arg | No hooks but accepts CLI prompt arg. Gets beacon in prompt + delayed work nudge. |
| **Auggie** | arg | Same pattern as Cursor. |
| **AMP** | arg | Same pattern as Cursor. Has `--dangerously-allow-all --no-ide`. |

**Pattern:** Agents with `SupportsHooks=true` get reliable assignment delivery. Agents without hooks all share the same fragility — they depend on the blind-sleep + fire-and-forget nudge mechanism.

**Codex is uniquely fragile** because it's the only agent with BOTH `SupportsHooks=false` AND `PromptMode="none"`. Cursor/Auggie/AMP at least get the beacon as a CLI prompt argument.

---

## What Gaps Remain

### Gap 1: No Active Readiness Detection for Non-Claude Agents

**Location:** `tmux.go:1727-1737`

Only Claude has `ReadyPromptPrefix` for active polling. All other agents use blind `time.Sleep()`. There is no universal "agent is ready to receive input" signal.

### Gap 2: AcceptBypassPermissionsWarning Runs Unconditionally on Fresh Spawns

**Locations:** `session_manager.go:376`, `daemon/lifecycle.go:397`, `daemon/daemon.go:1645`, `deacon/manager.go:161`, `refinery/manager.go:199`, `witness/manager.go:206`

The `EmitsPermissionWarning` guard only exists in `sling_helpers.go:880-891`. All fresh spawn and daemon restart paths call it unconditionally, wasting 1s for every non-Claude agent.

### Gap 3: Nudge Delivery Has No Acknowledgment

**Location:** `tmux.go:958`

Nudges are fire-and-forget `send-keys`. The retry mechanism (`sendKeysLiteralWithRetry`, line 911) handles transient "not in a mode" errors with exponential backoff — but it cannot detect if the agent's TUI was simply not ready to accept input. If keystrokes are accepted by tmux but not processed by the TUI, they're silently lost.

### Gap 4: VerifySurvived Only Checks tmux Session, Not Agent Process

**Location:** `session_manager.go:409-415`

Uses `HasSession()` instead of `IsAgentAlive()`. The `IsAgentAlive()` function exists (tmux.go:1616-1618, used by zombie detection) and checks `pane_current_command` against `GT_PROCESS_NAMES`, but it's NOT called during startup verification.

### Gap 5: Polecat Startup Diverges from Unified Lifecycle

**Location:** `session_manager.go` vs `session/lifecycle.go`

Dog sessions use the unified `StartSession()` with clean boolean flags. Polecat startup lives in its own `session_manager.go` with inline logic. This means fixes to one path don't apply to the other.

### Gap 6: No Guaranteed Post-Spawn Assignment Delivery for Non-Hook Agents

**Location:** `runtime.go:207`

For hook agents, `SessionStart` hook delivers `gt prime` reliably. For non-hook agents, assignment delivery depends entirely on the nudge timing window. The `StartupNudgeDelayMs` (2s) assumes `gt prime` completes in under 2s — but no verification occurs.

### Gap 7: Redundant Double Readiness Wait

**Location:** `session_manager.go:379` + `polecat_spawn.go:287`

`SleepForReadyDelay` in session_manager.go AND `WaitForRuntimeReady` in polecat_spawn.go both execute for the same polecat spawn. For Codex this means 3s + 3s = 6s of blind sleeping.

### Gap 8: Claude-Specific Hardcodings (Leak Points)

From `docs/design/agent-provider-interface.md`:

| Location | Issue |
|----------|-------|
| `runtime.go:97` | `SessionIDFromEnv()` falls back to `CLAUDE_SESSION_ID` |
| `types.go:509` | `BuildCommandWithPrompt()` special-cases `opencode` for `--prompt` flag |
| `sling_helpers.go:884` | Default agent name falls back to `"claude"` when GT_AGENT is empty |
| `types.go:562` | `normalizeRuntimeConfig()` defaults empty Provider to `"claude"` |
| `types.go:652` | `defaultRuntimeCommand()` falls back to `resolveClaudePath()` for unknown providers |

---

## Potential Remedies (Found in Codebase)

### Immediate Fixes (Low Risk)

1. **Guard `AcceptBypassPermissionsWarning` with `EmitsPermissionWarning`** in all fresh spawn paths. The fix pattern already exists in `sling_helpers.go:880-891` — it just needs porting to `session_manager.go:376` and the daemon/deacon/refinery/witness managers.

2. **Replace `HasSession` with `IsAgentAlive` in startup verification** (session_manager.go:409). `IsAgentAlive()` already exists and checks agent process liveness.

3. **Remove redundant `WaitForRuntimeReady`** in `polecat_spawn.go:287`. `session_manager.Start()` already performs readiness waiting.

4. **Increase `ReadyDelayMs` for Codex** from 3000 to 5000+ (simple, reduces the race window).

### Medium-Term Improvements

5. **Embed assignment in `AGENTS.md` before spawn.** Codex reads `AGENTS.md` on startup. Writing beacon content to the `AGENTS.md` file before launching ensures assignment delivery is filesystem-based (reliable) rather than keystroke-based (fragile). The `InstructionsFile` field already exists in `AgentPresetInfo`.

6. **Add nudge delivery verification.** After sending nudge, poll pane content (via `CapturePane`) to confirm the text appeared. Retry if not. The `sendKeysLiteralWithRetry` mechanism already handles tmux-level retries — this would add application-level verification.

7. **Add `ReadyPromptPrefix` for agents with detectable prompts.** If Codex TUI has a detectable ready indicator, use it for active polling. Copilot already has `ReadyPromptPrefix: ">"` set.

### Architectural Improvements

8. **Unify polecat startup with `session/lifecycle.go`.** Consolidate the two parallel startup paths so fixes apply uniformly across all session types.

9. **Implement the Agent Factory Worker Interface** described in `docs/design/agent-provider-interface.md`:
   - Tier 1 (Required): `start()`, `isReady()`, `isAlive()`, `sendMessage()`, `getStatus()`
   - Tier 2 (Preferred): `injectContext()`, `onSessionStart()`, `resume()`, `sessionID()`
   - Tier 3 (Advanced): `forkSession()`, `exec()`, `getUsage()`

10. **Add a sentinel-file readiness protocol.** Agent writes a marker file after init; startup polls for it. Provider-neutral, no TUI dependency.

---

## Key File Reference

| File | Purpose |
|------|---------|
| `internal/config/agents.go` | AgentPresetInfo registry (9 presets) |
| `internal/config/types.go` | RuntimeConfig struct, normalizeRuntimeConfig() |
| `internal/config/loader.go` | Resolution chain: role → rig → town → preset → claude |
| `internal/runtime/runtime.go` | StartupFallbackInfo matrix, hook installer registry |
| `internal/polecat/session_manager.go` | Polecat spawn sequence (190-433) |
| `internal/cmd/sling.go` | Unified sling dispatch (155+) |
| `internal/cmd/polecat_spawn.go` | SpawnPolecatForSling, StartSession |
| `internal/cmd/sling_helpers.go` | ensureAgentReady, shouldAcceptPermissionWarning |
| `internal/tmux/tmux.go` | WaitForRuntimeReady, NudgeSession, AcceptBypassPermissionsWarning |
| `internal/session/lifecycle.go` | Unified StartSession (non-polecat) |
| `internal/session/startup.go` | BeaconConfig, BuildStartupPrompt |
| `internal/daemon/daemon.go` | checkPolecatHealth, GUPP violation checks |
| `internal/wrappers/scripts/gt-codex` | Wrapper: runs gt prime before exec codex |
| `docs/design/agent-provider-interface.md` | Design doc: Agent Factory Worker Interface |
| `docs/agent-provider-integration.md` | Integration guide: 4-tier model |

---

## Prior Art

- `throw_away/codex-startup-investigation.md` — Codex crash chain analysis (gt-f0u8)
- `docs/design/agent-provider-interface.md` — Agent Factory Worker Interface design doc (3-tier capability model)
- `docs/agent-provider-integration.md` — Integration guide with 4-tier integration model
- `docs/HOOKS.md` — Claude Code-centric hooks documentation
