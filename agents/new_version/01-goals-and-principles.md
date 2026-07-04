# 1. Goals and Guiding Principles

## 1.1 Mission

A single person owns multiple cloud storage accounts across different providers. Individually,
each account is small, untrusted to never fail, and limited in space. Together, they should behave
like one larger, redundant, self-healing storage pool that the user controls from the command line.

The tool exists to make that pool real and to keep it healthy automatically.

## 1.2 What success looks like

The tool delivers value when it reliably achieves all of the following for the user:

1. **Redundancy** — Every file the user cares about exists, intact, on more than one account and,
   wherever possible, on more than one provider. Losing any single account never loses data.
2. **Capacity** — The user's primary account is kept as empty as possible by offloading its
   contents to backup accounts, so the primary always has room to receive new work.
3. **No waste** — Duplicate copies of the same file within a single account are detected and can be
   removed, so paid/limited space is not wasted on accidental duplication.
4. **Balance** — No single backup account fills up while others sit empty. Storage pressure is
   spread out so the pool degrades gracefully as it grows.
5. **Consistency** — The same logical set of files and folder structure is visible across all
   participating accounts, so the user sees one coherent library regardless of which account they
   look at.
6. **Safety** — The tool never modifies, moves, or deletes anything outside the area it was given
   permission to manage, and never destroys data it has not first confirmed is safely copied
   elsewhere.

## 1.3 Guiding principles

These principles govern every decision the tool makes. When two requirements seem to conflict,
resolve the conflict in favor of the principle that appears earliest in this list.

1. **Do no harm.** Destructive actions (delete, overwrite, ownership change) are only ever taken
   after the tool has positively confirmed the data is preserved elsewhere. Never delete an
   original before its replacement is verified.
2. **Stay inside the fence.** The tool only ever reads, lists, writes, moves, or deletes content
   inside the specific managed areas it created or was pointed at. Everything else in the user's
   accounts is invisible to it and must remain untouched.
3. **Be resumable and idempotent.** Any operation can be interrupted (crash, network loss, rate
   limit) and safely re-run. Re-running a completed operation changes nothing. The tool converges
   toward the correct state rather than assuming it starts from a clean one.
4. **Trust the user's password, store nothing in the clear.** All secrets and all tracked state
   live encrypted at rest, unlocked only by a master password the user supplies at runtime.
5. **Be scriptable.** Every capability can run unattended: inputs can be supplied non-interactively,
   output is informative, and the process signals success or failure unambiguously to its caller.
6. **Be honest about what it did.** The tool logs the meaningful actions it takes and the errors it
   hits, clearly attributing each to the provider and account involved. A user reading the log can
   reconstruct what happened.
7. **Prefer the provider's native capability, but never depend on it.** When a provider offers a
   feature that achieves a goal more cheaply or safely, use it — but always have a fallback that
   achieves the same outcome when the native feature is unavailable or fails.

## 1.4 Explicit non-goals

- This is **not** a general-purpose file sync between arbitrary folders. It manages one specific
  area per account and nothing else.
- This is **not** a backup-to-local tool. The redundancy it provides is across cloud accounts.
- It does **not** need a graphical interface. It is a command-line tool intended for power users
  and scheduled automation.
- It is **not** required to support providers beyond the three named here, but its design should
  not make adding a future provider unreasonably hard.
