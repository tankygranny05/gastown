# Investigation: Who Killed Polecat Furiosa (ec3f7fe2)
*[Created by Codex: 019c813d-7f92-7720-82cf-22cb95582a4e 2026-02-22]*

## Executive Conclusion

- **Who killed furiosa:** `ec3f7fe2-db7b-4825-93a2-4021a1d23d9a` (**furiosa itself**)
- **When:** `2026-02-22T00:04:41.592+07:00` command issued; last alive at `2026-02-22T00:04:46.189+07:00`
- **UTC:** command at `2026-02-21T17:04:41.592Z`; last alive at `2026-02-21T17:04:46.189Z`
- **With what command:**  
  `cd /Users/sotola/gt/codex_rs_0_104_0/polecats/furiosa/codex_rs_0_104_0 && gt done 2>&1`

I found **no external kill/nuke command** against `cr010-furiosa` or `codex_rs_0_104_0/furiosa` inside the murder window.

## Timeline (Ground Truth)

- Victim working session (`SID ec3f7fe2`, PID 21338) round end:
  - `Time: 2026-02-21T23:54:15.526 -> 2026-02-22T00:04:46.189` (local `+07`)
- Final victim tool call before death:
  - `2026-02-22T00:04:41.592+07:00` -> `gt done`
- Resurrection prompt arrives later in new PID session (15202):
  - `2026-02-22T00:20:34.318+07:00` user: `Did you run gt done ? Your Agent Id is: ec3f7fe2...`
- Murder window from assignment logic:
  - `00:04:46.189 -> 00:20:34.318` local (`+07`)
  - duration: `15m 48.129s`

## Suspect Sweep

I queried active sessions around `T=2026-02-22T00:04:46` and dumped all suspects.

High-signal findings:

- `1ede9d81-faa1-479c-b577-5026be6d5f33` did run:
  - `tmux kill-session -t gt-furiosa ...` at `2026-02-21T23:57:21.449+07:00`
  - This is **not** `cr010-furiosa` and happened earlier.
  - Same session then checked `gt polecat status codex_rs_0_104_0/furiosa` at `00:01:41` and saw furiosa still running.
- `b7ff9fb4-...` (deacon patrol) explicitly **skipped session-gc** to protect active `cr010-furiosa`, then observed it as done/dead after the victim’s `gt done`.
- Boot triage sessions (`100db9c6`, `819b0ada`) had no kill/nuke actions.

## Command-Level Window Verification

I ran a direct `items + deltas` DB join scan for `00:04:41 -> 00:09:50` looking for:
- `kill-session`
- `tmux kill`
- `gt polecat nuke`
- `gt polecat kill`
- `kill -9`, `kill -TERM`
- `session-gc`
- `gt rig stop/restart`

Result: **no matching external kill commands** in the interval.

The only terminal command at the death edge was the victim’s own `gt done`.

## Why It Happened

This appears to be the known failure mode: **polecat self-termination during `gt done` path before full completion bookkeeping converges** (branch pushed/commit exists, but post-`gt done` lifecycle state ended inconsistent and required manual recovery/cherry-pick).

Classification:
- Not witness patrol kill
- Not deacon session-gc kill
- Not manual external tmux kill of `cr010-furiosa`
- **Self-kill during `gt done` execution path**

## Prevention Recommendation

1. Harden `gt done` lifecycle ordering so self-destruct happens only after durable success checkpoints (`POLECAT_DONE`, MR-bead creation/ack, state transition).
2. Add structured telemetry around `gt done` phases (`entered`, `pushed`, `mr_created`, `witness_notified`, `self_exit`) with timestamps.
3. Add witness guardrail: if `session dead` + `bead still working`, auto-mark as `RECOVERY_NEEDED` with culprit phase from telemetry.
4. Add a temporary canary check in patrol: detect `commit exists + no gt done completion` and notify before nuke actions.

