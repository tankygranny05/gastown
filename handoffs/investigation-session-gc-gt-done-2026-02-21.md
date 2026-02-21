# Investigation: Session-GC Killing Polecats + gt done MR Failures

**Date:** 2026-02-21
**Investigator:** polecat nux (gastown)
**Issue:** gt-t3tx
**Status:** Research complete — recommendations only, no code changes

---

## Executive Summary

Two connected failure modes caused cascading work loss on 2026-02-21:

1. **session-gc killed active rig agents** (witness, refinery, polecats with committed work)
2. **gt done completes but MR bead never filed** (recurring pattern all day)

Root cause: session-gc delegates to `gt doctor --fix` which applies zombie and orphan
checks **indiscriminately** — it has no concept of "permanent rig agents" vs "ephemeral
polecats." When rig agents (witness/refinery) crashed due to the cwd bug (gt-9znt), their
tmux sessions were left behind as zombies. session-gc nuked them, preventing recovery.

The two failures ARE connected: a session killed mid-`gt done` results in the MR bead
never being created, since `gt done` is a multi-step process (push → create MR bead →
merge Dolt branch → notify witness).

---

## Failure Mode 1: session-gc Killed Active Polecats and Rig Agents

### What Happened

On the 7th patrol pass, `mol-session-gc` (running in conservative mode) dispatched a dog
that ran `gt doctor --fix`. This killed:

- `codex_rs_0_104_0` rig: witness + refinery + polecat furiosa (which had COMMITTED work
  on cr010-ll5 but hadn't run `gt done` yet)
- `agent_box_v1` rig: witness + refinery
- `claude_code_2_1_47` rig: witness + refinery

### Classification Logic

#### Zombie Sessions (`internal/doctor/zombie_check.go`)

**File:** `internal/doctor/zombie_check.go:33-109`

The `ZombieSessionCheck` classifies a session as zombie when:
1. The session name matches Gas Town naming patterns (`session.IsKnownSession(sess)` at line 64)
2. The session is NOT a crew session (line 70 — `isCrewSession(sess)`)
3. `t.IsAgentAlive(sess)` returns false (line 75)

`IsAgentAlive` (`internal/tmux/tmux.go:1616-1618`) calls `IsRuntimeRunning` which:
1. Gets the pane command via `GetPaneCommand(session)`
2. Checks if it matches expected process names (claude, node, codex, etc.)
3. If the pane shows a shell, checks descendant processes for those names

**Critical gap:** If Claude crashed but the tmux session survived (shell still running),
the session is classified as a zombie. This is correct for polecats but **destructive for
rig agents** (witness/refinery) which should be restarted, not killed.

#### Orphan Sessions (`internal/doctor/orphan_check.go`)

**File:** `internal/doctor/orphan_check.go:57-131`

The `OrphanSessionCheck` classifies a session as orphan when:
1. The session parses as a Gas Town session (`session.ParseSessionName(sess)`)
2. The session's rig name doesn't match any valid rig in the filesystem (lines 241-264)

`isValidSession` (line 209) checks:
- Mayor, deacon, boot sessions → always valid
- Rig sessions → valid if the rig directory exists AND has a `polecats/` or `crew/` subdirectory

**Both checks have the same deficiency:** They do not distinguish between polecat sessions
(ephemeral, safe to kill) and rig agent sessions (witness/refinery — should never be
auto-killed by GC).

### The Fix Function

**Zombie Fix** (`zombie_check.go:113-145`):
- Re-verifies Claude is still dead (TOCTOU guard at line 130)
- Never auto-kills crew sessions (line 123)
- Kills everything else via `t.KillSessionWithProcesses(sess)`

**Orphan Fix** (`orphan_check.go:135-159`):
- Never auto-kills crew sessions (line 146)
- Kills everything else via `t.KillSessionWithProcesses(sess)`

**Missing safety guard:** Neither fix function checks for witness or refinery sessions.
Only crew sessions are protected from auto-kill.

### Why Were Rig Agents Classified as Zombies?

The gt-9znt bug (`internal/daemon/lifecycle.go`) caused the daemon's `restartSession`
to use incorrect working directories for witness and refinery:

- **Witness:** Was started in rig root instead of `witness/rig/` — this caused identity
  confusion and likely a crash on startup
- **Refinery:** Was started with a non-existent `refinery/rig/` path, causing immediate failure

When these agents crashed, their tmux sessions remained as shells with no Claude process
running. `gt doctor` correctly detected them as zombies (tmux alive, Claude dead) and
`gt doctor --fix` killed them.

The gt-9znt fix (commit `61565815`) corrected the `getWorkDir` logic to match the
resolution order in `witness.Manager.witnessDir()` and `refinery.Manager.Start()`.
However, the session-gc still lacks protection against killing permanent rig agents.

### session-gc Decision Flow

**File:** `internal/formula/formulas/mol-session-gc.formula.toml`

The mol-session-gc formula is executed by a dog (worker process) when the deacon's patrol
step "session-gc" (line 777 of `mol-deacon-patrol.formula.toml`) detects cleanup needs.

The flow:
1. **Deacon patrol step** (`session-gc`, line 777): Runs `gt doctor -v` to preview, then
   dispatches dog with `gt sling mol-session-gc deacon/dogs --var mode=conservative`
2. **Dog executes** `mol-session-gc` formula:
   - Step `determine-mode`: Reads mode from hook_bead (conservative vs aggressive)
   - Step `preview-cleanup`: Runs `gt doctor -v` to identify targets
   - Step `execute-gc`: Runs `gt doctor --fix` — **this is where the kill happens**
   - Step `verify-cleanup`: Re-runs `gt doctor -v` to confirm

**No additional filtering occurs between doctor's classification and the fix.**
The formula's safety checks (lines 137-140) say "Never delete active sessions" but
rely entirely on `gt doctor`'s classification logic, which doesn't distinguish rig
agent types.

---

## Failure Mode 2: gt done Completes but MR Never Filed

### gt done Code Path

**File:** `internal/cmd/done.go:81-1037`

The full `gt done` flow for COMPLETED status:

```
1. Validate role (polecat only) and exit status       [lines 82-95]
2. Set up deferred session cleanup (backstop)         [lines 113-135]
3. Set up SIGTERM handler                             [lines 142-154]
4. Find workspace and determine rig                   [lines 159-213]
5. Detect git state and branch                        [lines 215-296]
6. Parse branch info, determine issue ID              [lines 298-345]
7. Write done-intent label EARLY                      [lines 347-363]
8. Verify commits exist ahead of default branch       [lines 383-465]
9. Determine merge strategy (direct/mr/local)         [lines 467-531]
10. Push branch to origin                              [lines 545-625]
11. Create MR bead (3 retry attempts)                  [lines 756-837]
12. Merge Dolt branch to main                          [lines 864-892]
13. Nudge refinery                                     [lines 899-901]
14. Notify witness                                     [lines 903-968]
15. Update agent state, clear hook                     [lines 979-1037]
16. Self-nuke worktree + kill session                  [lines 981-1027]
```

### The "command_injection_detected" Error

The string "command_injection_detected" does NOT appear anywhere in the gastown codebase.
This error likely originates from an external tool or service that gt done interacts with,
or from a Claude Code safety mechanism that detected shell metacharacters in the branch
name or commit message being passed to git commands.

Possible sources:
- Claude Code's own command sanitization (detects shell injection patterns in arguments)
- A git hook or CI check that validates input
- The beads CLI (`bd`) rejecting input with suspicious characters

The `isValidBeadsPrefix` function in `internal/rig/manager.go:1031` validates prefixes
but this runs at rig init time, not during gt done. This is a **separate safety check**
for config files, not the runtime error polecats hit.

**Recommendation:** Search agent session logs for the exact error context to identify
which external tool produced this message.

### MR Bead Creation Failures

**File:** `internal/cmd/done.go:777-819`

MR bead creation uses `bd.Create()` with 3 retry attempts (lines 779-794). Each retry
waits `attempt*2` seconds before retrying.

If all 3 attempts fail (line 795):
1. Sets `mrCreationFailed = true` — prevents deferred self-kill (gt-t79 fix)
2. Sets `pushFailed = true` — prevents worktree nuke
3. Writes `CheckpointMRFailed` label on agent bead for resume
4. Emits `done_mr_failed` event
5. Falls through to notify witness with error details

**Recent hardening commits:**
- `09b632b4` — fix: gt done silently fails to create MR bead (gt-cof)
- `f14dce0c` — fix: prevent gt done from self-killing when MR creation fails (gt-t79)
- `01fa88fc` — fix(done): harden MR failure resume path
- `b1ed0cbe` — fix(done): restore exit-code contract and fix dead-code logging
- `0b614692` — fix(done): preserve validation errors instead of swallowing them

### Why Did MR Beads Fail to Be Created?

Common causes based on code analysis:

1. **Dolt lock contention**: Multiple polecats writing to beads simultaneously. The
   `bd.Create()` call touches the Dolt database. Under load, lock contention causes
   transient failures. The 3-retry with backoff helps but may not be enough.

2. **Dolt branch merge race**: At line 875, gt done merges the polecat's Dolt branch
   to main. If this fails, the MR bead is stranded on the polecat's branch. The refinery
   reads from main and never sees the MR.

3. **Session killed mid-gt-done**: If session-gc kills the polecat's tmux session while
   gt done is executing (between push at step 10 and MR creation at step 11), the process
   is terminated before the MR bead is created. The SIGTERM handler (lines 142-154) only
   runs if the process receives SIGTERM — if tmux kills the session with SIGKILL or by
   destroying the session, the handler may not run.

4. **Context exhaustion**: Claude Code may kill the gt process via SIGTERM when context
   runs out, interrupting gt done mid-execution.

### Older Convoys (Still Open)

- **hq-cv-7nnjw**: "Harden gt done: MR creation failure must not silently self-kill"
  - Tracks: gt-t79 (prevent self-kill on MR failure)
  - Depends on: gt-mol-t79 (mol-witness-patrol)
  - Status: **OPEN** — underlying molecule still in progress

- **hq-cv-f3pc6**: "gt done silently fails to create MR bead after successful push"
  - Tracks: gt-cof (silent MR bead failure)
  - Depends on: external:gastown:gt-cof (unresolved external dependency)
  - Status: **OPEN** — external dependency unresolved

Both convoys are about different aspects of the same MR creation failure mode. The fixes
(commits 09b632b4, f14dce0c, 01fa88fc) addressed the most severe symptoms (silent
self-kill, missing retry) but the root causes (Dolt lock contention, session kill during
gt done) are not fully mitigated.

---

## Timeline: The Cascade (2026-02-21)

```
[Before today]
  - gt-9znt bug: daemon restartSession uses wrong cwd for witness/refinery
  - When daemon restarts rig agents, they crash immediately due to wrong cwd
  - Crashed sessions leave zombie tmux sessions behind

[Today - early hours]
  1. Multiple rigs have zombie witness/refinery sessions from the cwd bug
  2. Deacon patrol runs session-gc (7th pass, conservative mode)
  3. session-gc dispatches dog → dog runs gt doctor --fix
  4. gt doctor classifies zombie sessions (witness/refinery included)
  5. gt doctor --fix kills ALL zombie sessions, including:
     - witness + refinery for codex_rs, agent_box_v1, claude_code rigs
     - polecat furiosa (had committed work, hadn't run gt done yet)
  6. Without witness/refinery, polecats have no supervision or merge capability
  7. furiosa's committed work on cr010-ll5 is stranded (never gt done'd)

[Today - throughout the day]
  8. Multiple polecats across rigs attempt gt done
  9. Some hit "command_injection_detected" (source TBD — likely external tool)
  10. Others succeed at push but fail at MR bead creation (Dolt contention)
  11. abv polecats: 3 features had to be manually merged
  12. gt-9znt fix is committed (61565815) fixing the cwd bug
  13. Rig agents can now restart correctly, but damage is done
```

---

## Connection Between the Two Failures

**Hypothesis: CONFIRMED — the failures are connected.**

The connection is:

1. **Direct kill pathway**: session-gc killing a polecat mid-`gt done` means the MR bead
   is never created. The branch is pushed to origin but nobody processes it.

2. **Indirect cascade**: session-gc killing witness/refinery removes the supervisory layer.
   Without a witness, polecat failures go undetected. Without a refinery, successfully
   submitted MR beads can't be merged.

3. **Recovery failure**: The gt-9znt cwd bug meant that even when the daemon tried to
   restart witness/refinery, they'd crash again immediately. This created a
   kill → restart → crash → zombie → kill loop.

The gt-9znt fix broke the loop by fixing the cwd, but the fundamental vulnerability
remains: session-gc can kill permanent rig agents.

---

## Root Cause Analysis

### Root Cause 1: session-gc lacks role-based kill protection

**Severity: CRITICAL**

`gt doctor`'s zombie and orphan checks protect crew sessions (via `isCrewSession()`) but
do NOT protect witness or refinery sessions. These are permanent infrastructure agents
that should be restarted, never killed by GC.

**Files:**
- `internal/doctor/zombie_check.go:70` — only skips crew sessions
- `internal/doctor/orphan_check.go:146` — only skips crew sessions

### Root Cause 2: session-gc is indiscriminate (no role awareness)

**Severity: HIGH**

The `mol-session-gc` formula simply runs `gt doctor --fix`. It has no concept of:
- Agent roles (polecat vs witness vs refinery)
- Active work status (hooked work, in-progress beads)
- Done-intent labels (polecat was trying to gt done)

### Root Cause 3: gt done is not atomic

**Severity: HIGH**

`gt done` performs multiple sequential operations (push, create MR, merge Dolt, notify)
without transactional guarantees. If interrupted at any point, work can be in an
inconsistent state:
- Branch pushed but no MR → orphaned branch
- MR created but Dolt not merged → refinery can't see MR
- Dolt merged but witness not notified → polecat marked as working forever

The checkpoint system (gt-aufru) mitigates this by enabling resume, but it requires
the polecat to be alive to retry. If the session was killed, no retry happens.

### Root Cause 4: "command_injection_detected" is opaque

**Severity: MEDIUM**

This error appears to come from outside the gastown codebase. The exact source needs to
be identified — it may be Claude Code's own safety mechanism, a git hook, or `bd`'s
input validation. Whatever it is, it blocks gt done without a clear fix path.

---

## Recommendations (Code-Level)

### R1: Add witness/refinery protection to gt doctor checks

**Files to modify:**
- `internal/doctor/zombie_check.go` — Add `isRigAgentSession(sess)` check alongside
  `isCrewSession(sess)` at line 70
- `internal/doctor/orphan_check.go` — Add same check at line 146
- `internal/doctor/orphan_check.go` — Add helper function:
  ```go
  func isRigAgentSession(sess string) bool {
      identity, err := session.ParseSessionName(sess)
      if err != nil { return false }
      return identity.Role == session.RoleWitness || identity.Role == session.RoleRefinery
  }
  ```

**Impact:** Rig agents will never be killed by `gt doctor --fix`. They'll still be
REPORTED as zombies (for visibility) but the Fix function won't touch them.

### R2: Add polecat work-in-progress guard

**Files to modify:**
- `internal/doctor/zombie_check.go` — Before killing a polecat session, check if the
  agent bead has a `done-intent:*` label or `hook_bead` set. If so, skip kill.

**Rationale:** A polecat with done-intent was trying to `gt done` — killing it mid-flight
causes the exact MR creation failure we're seeing.

### R3: Make session-gc dog check agent beads before killing

**Files to modify:**
- `internal/formula/formulas/mol-session-gc.formula.toml` — Add a step between
  "preview-cleanup" and "execute-gc" that checks each zombie session's agent bead for
  active work, done-intent, or permanent role status.

### R4: Add atomic MR creation with server-side Dolt

**Files to modify:**
- `internal/cmd/done.go` — Consider using `doltserver.MergePolecatBranch` BEFORE
  creating the MR bead (swap steps 11 and 12). This way, the MR bead is created on
  main directly, visible to refinery even if the polecat dies immediately after.

**Alternative:** Create the MR bead on main (not the polecat's Dolt branch) so it's
immediately visible.

### R5: Investigate "command_injection_detected" source

**Action:** Search agent session logs (sse_lines.jsonl, .events.jsonl) for the exact
error context. Determine if this is Claude Code's safety mechanism, a git hook, or
bd's validation. Filed as separate investigation need.

### R6: Add rig-agent restart instead of kill to gt doctor

**Files to modify:**
- `internal/doctor/zombie_check.go` — For witness/refinery zombies, instead of killing,
  send a lifecycle restart request to the daemon. This leverages the existing
  `restartSession` logic (which now has the gt-9znt fix).

---

## Appendix: Key Source Files Referenced

| File | Line(s) | Description |
|------|---------|-------------|
| `internal/cmd/done.go` | 81-1037 | Full gt done implementation |
| `internal/cmd/done.go` | 113-135 | Deferred session cleanup (backstop) |
| `internal/cmd/done.go` | 347-363 | Done-intent label (early write) |
| `internal/cmd/done.go` | 777-819 | MR bead creation with retry |
| `internal/cmd/done.go` | 864-892 | Dolt branch merge |
| `internal/cmd/done.go` | 981-1027 | Self-nuke + session kill |
| `internal/doctor/zombie_check.go` | 33-145 | Zombie session detection + fix |
| `internal/doctor/orphan_check.go` | 57-159 | Orphan session detection + fix |
| `internal/tmux/tmux.go` | 1574-1631 | IsRuntimeRunning / IsAgentAlive |
| `internal/daemon/lifecycle.go` | 39-100 | Lifecycle request processing |
| `internal/daemon/lifecycle.go` | 342-401 | Session restart logic |
| `internal/daemon/lifecycle.go` | 403-453 | getWorkDir (fixed by gt-9znt) |
| `internal/daemon/lifecycle.go` | 1031-1138 | Orphaned work detection |
| `internal/formula/formulas/mol-session-gc.formula.toml` | 1-294 | session-gc formula |
| `internal/formula/formulas/mol-deacon-patrol.formula.toml` | 777-806 | Patrol session-gc step |

---

*Investigation by polecat nux (gastown) — 2026-02-21*
*Agent ID: e9fd92e9-ab79-42e6-b3e1-32e13f39beb4*
