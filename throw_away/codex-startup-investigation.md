<!-- [Created by Claude: a870463b-f3dc-41de-9967-e4ad8b288223] -->

# Codex Polecat Startup Crash Investigation (gt-f0u8)

## Symptoms

Codex polecats consistently crash during startup with:
- `"timeout waiting for runtime prompt"` (from `tmux.go:1756`)
- `"session likely died during startup"` (from `polecat_spawn.go:315`)

Claude polecats on the same rigs work fine.

## Root Cause Analysis

### The Error Chain

The crash originates from a **double readiness check** with different semantics for Codex vs Claude:

1. **`session_manager.go:379`** — `runtime.SleepForReadyDelay(runtimeConfig)` — sleeps `ReadyDelayMs` (3000ms for Codex)
2. **`polecat_spawn.go:287`** — `t.WaitForRuntimeReady(sessionName, runtimeConfig, 30*time.Second)` — called AFTER `Start()` returns

### Why Claude Succeeds

Claude has `ReadyPromptPrefix: "❯ "` set in its agent preset (`agents.go:181`). The `WaitForRuntimeReady` function (`tmux.go:1722`) takes the **prompt-polling path**: it polls the tmux pane for the `❯` prompt prefix and returns as soon as it appears. This is robust — it actively verifies the agent is ready.

### Why Codex Fails

Codex has **no `ReadyPromptPrefix`** (empty string in `agents.go:209-226`). The `WaitForRuntimeReady` function takes the **delay-only fallback path** (`tmux.go:1727-1737`):

```go
if rc.Tmux.ReadyPromptPrefix == "" {
    if rc.Tmux.ReadyDelayMs <= 0 {
        return nil
    }
    delay := time.Duration(rc.Tmux.ReadyDelayMs) * time.Millisecond
    time.Sleep(delay)
    return nil
}
```

This means for Codex:
- `SleepForReadyDelay` in `session_manager.go:379` sleeps **3 seconds**
- Then nudges are sent (beacon + 2s delay + startup nudge = ~2+ seconds)
- Then `Start()` returns
- Then `polecat_spawn.go:287` calls `WaitForRuntimeReady` which sleeps **another 3 seconds**

Total: ~8+ seconds of blind sleeping. The `WaitForRuntimeReady` call in `polecat_spawn.go` is **redundant** — `Start()` already called `SleepForReadyDelay` internally.

### The Actual Crash: Session Dies Between Checks

The real failure is at `polecat_spawn.go:311`:

```go
pane, err := getSessionPane(s.SessionName)
if err != nil {
    _ = t.KillSession(s.SessionName)
    return "", fmt.Errorf("getting pane for %s (session likely died during startup): %w", s.SessionName, err)
}
```

The Codex binary crashes **during or shortly after startup**, and by the time `getSessionPane` is called (~8s later), the tmux pane is dead. The `session_manager.go:409-415` post-startup survival check passes because the session still technically exists in tmux, but the pane process has exited.

### Why Does Codex Crash?

Key differences that make Codex fragile at startup:

1. **No `SupportsHooks`** — Codex can't run `gt prime` via a SessionStart hook. Instead, the beacon nudge tells it to "Run `gt prime`". If the nudge arrives before Codex is ready to accept input, it's lost.

2. **`AcceptBypassPermissionsWarning`** (`session_manager.go:376`) — This function waits 1 second and checks for "Bypass Permissions mode" text. Codex uses `--dangerously-bypass-approvals-and-sandbox` which has a **different warning text** or no warning at all. The function is Claude-specific but runs for all agents, wasting 1 second.

3. **`WaitForCommand` with `SupportedShells` exclusion** (`session_manager.go:373`) — This waits up to `ClaudeStartTimeout` (60s!) for the pane command to NOT be a shell. For Codex, the binary name is `codex`, which isn't in `SupportedShells`, so this returns immediately. But the timing gap means nudges may be sent before Codex's TUI is ready.

4. **`ReadyDelayMs: 3000`** — Codex's 3-second delay may be insufficient. Codex is a Rust binary with a TUI (Ratatui) that needs to:
   - Initialize the runtime
   - Authenticate (API key or OAuth)
   - Set up the sandbox (Seatbelt on macOS)
   - Render the initial TUI

   On slower machines or under load (multiple polecats), 3 seconds may not be enough.

### "test_rig" Is Not a Real Rig

There is **no `test_rig` configuration** in the codebase. The string `testrig` only appears in unit test fixtures (`namepool_test.go`, `manager_test.go`, `startup_test.go`). The bead description's reference to "test_rig" likely means the issue was observed on **any rig** when `--agent codex` is used, or refers to a transient rig created for testing.

## Timing Breakdown: Codex Startup Sequence

| Step | Duration | Cumulative | Location |
|------|----------|------------|----------|
| `NewSessionWithCommand` | ~100ms | 0.1s | `session_manager.go:303` |
| `SetEnvironment` (multiple) | ~200ms | 0.3s | `session_manager.go:318` |
| `WaitForCommand` (shell exclusion) | ~0ms (returns immediately for Codex) | 0.3s | `session_manager.go:373` |
| `AcceptBypassPermissionsWarning` | 1s (sleep) + check | 1.3s | `session_manager.go:376` |
| `SleepForReadyDelay` | 3s | 4.3s | `session_manager.go:379` |
| Send beacon nudge | ~50ms | 4.35s | `session_manager.go:390` |
| Wait for gt prime | 2s | 6.35s | `session_manager.go:395` |
| Send startup nudge | ~50ms | 6.4s | `session_manager.go:400` |
| `RunStartupFallback` | varies | 6.5s | `session_manager.go:405` |
| Verify session survived | ~50ms | 6.5s | `session_manager.go:409` |
| **`Start()` returns** | | **~6.5s** | |
| `WaitForRuntimeReady` (redundant 3s sleep) | 3s | 9.5s | `polecat_spawn.go:287` |
| `SetAgentStateWithRetry` | varies | ~10s | `polecat_spawn.go:299` |
| `getSessionPane` | ~50ms | ~10s | `polecat_spawn.go:311` — **CRASH HERE** |

## Proposed Fix

### Short-term (workaround)

1. **Remove the redundant `WaitForRuntimeReady` in `polecat_spawn.go:287`**. `Start()` already handles readiness internally. The double-sleep wastes 3 seconds and increases the window for the session to die before `getSessionPane`.

2. **Increase `ReadyDelayMs` for Codex from 3000 to 5000** in `agents.go:225`. This gives Codex more time to initialize before nudges are sent.

### Medium-term (proper fix)

3. **Add `ReadyPromptPrefix` for Codex**. Codex's Ratatui TUI renders a prompt. Identify the prompt character and set it in the preset, so `WaitForRuntimeReady` can do active polling instead of blind sleeping.

4. **Guard `AcceptBypassPermissionsWarning` with `EmitsPermissionWarning`**. Currently it runs for all agents; it should be skipped for Codex (which has `EmitsPermissionWarning: false`).

```go
// session_manager.go:376 — change from:
debugSession("AcceptBypassPermissionsWarning", m.tmux.AcceptBypassPermissionsWarning(sessionID))
// to:
if runtimeConfig.Session != nil && runtimeConfig.Session.EmitsPermissionWarning {
    debugSession("AcceptBypassPermissionsWarning", m.tmux.AcceptBypassPermissionsWarning(sessionID))
}
```

This saves 1 second of wasted sleep for Codex and other non-Claude agents.

### Long-term

5. **Unify readiness detection** — Replace the blind-sleep fallback with an active readiness probe. Options:
   - Have agents write a sentinel file on ready (e.g., `/tmp/gt-ready-<session>`)
   - Use tmux pane process status (Codex changes from shell → codex when ready)
   - Poll for Codex-specific TUI elements in the pane

## Files Examined

- `internal/config/agents.go` — Agent presets (Claude vs Codex differences)
- `internal/polecat/session_manager.go` — Session creation and startup sequence
- `internal/cmd/polecat_spawn.go` — Spawn flow including redundant WaitForRuntimeReady
- `internal/tmux/tmux.go` — WaitForRuntimeReady, AcceptBypassPermissionsWarning
- `internal/runtime/runtime.go` — SleepForReadyDelay, GetStartupFallbackInfo
- `internal/config/loader.go` — BuildStartupCommandWithAgentOverride
- `internal/constants/constants.go` — ClaudeStartTimeout (60s)
