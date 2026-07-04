# 6. Commands (User-Facing Capabilities)

This document lists the capabilities the tool must expose to the user and **what each must
accomplish**. Names are suggestions for the user's convenience; the contract is the described
outcome, not the spelling. The exact command surface, flag names, and help text are yours to design,
provided each capability below is reachable and behaves as described.

Every capability that changes anything must honor the global behaviors in
[06.1](#61-behaviors-shared-by-all-commands).

## 6.1 Behaviors shared by all commands

- **Master password:** Required to unlock state. Accept it interactively or non-interactively.
- **Dry-run / safe mode:** Any state-changing capability must support a mode that performs no
  cloud writes, deletions, or permission changes, and instead reports exactly what it *would* do.
  Local read-only work and reading state may still occur.
- **Help:** Every capability can explain itself on request.
- **Pre-flight:** Any capability that touches the cloud first verifies the fence is well-formed for
  the accounts it will use (see [02-core-concepts.md](02-core-concepts.md)) and stops clearly if it
  is not.
- **State handling:** Capabilities that read state obtain the freshest available copy first;
  capabilities that change state publish the updated copy back to the pool afterward (see
  [04-data-and-state.md](04-data-and-state.md)).
- **Result signaling:** Succeed silently-enough to be scriptable and fail loudly with a non-success
  result and an explanatory message. The process's exit status must distinguish success from
  failure so it can drive automation.

## 6.2 Setup and account management

- **Initialize / set up the main account.** First-time setup: establish the master password and
  salt, collect provider application credentials, create the encrypted configuration and state,
  authenticate the main account, and ensure the main account's managed root exists. Re-running this
  later may update credentials or the main account. It should be possible to provide all credentials
  as structured input for scripting, and to emit the equivalent structured input for reuse.
- **Add a backup account.** Authorize and register an additional backup account on any provider.
  Must refuse if no main account exists yet. As part of adding, ensure the new account is wired into
  the pool correctly for its provider (e.g., gains access to the shared structure on Google, or gets
  its own managed root on Microsoft).
- **Remove a backup account.** Unregister an account from the configuration. Local-only; never
  deletes cloud data. A main account is removable only when no backup accounts depend on it.
- **Check credentials.** Report which accounts' stored credentials still work and which need
  re-authentication, across all providers.
- **Re-authenticate.** Repair broken credentials; by default only the broken ones, optionally all.
  Must confirm each re-authenticated identity matches the original account.
- **Repair sharing/permissions.** Verify and, where missing, restore the access each backup account
  needs to the shared managed structure (relevant to the provider that uses sharing).

## 6.3 Observing the pool

- **Scan / refresh metadata.** Recursively enumerate the managed area on every account and update
  the tracked state to match reality: record every file, replica, fragment, and folder, including
  references/placeholders standing in for files held elsewhere, and including Telegram's
  self-described files. This is the foundation other capabilities build on, and they should ensure
  it is current before acting.
- **Report quota.** For each provider, report total space used and total space available,
  aggregated across that provider's accounts, and sanity-check that usage on one provider could be
  accommodated by another — flag concerning imbalances.
- **Detect duplicates.** After refreshing, identify sets of files that are byte-identical copies
  **within the same provider** and present them grouped, so the user can see wasted space.

## 6.4 Healing and optimizing the pool

- **Remove duplicates (interactive).** For each duplicate set, let the user choose which copies to
  delete and remove the rest safely.
- **Remove duplicates (automatic).** For each duplicate set, keep the oldest copy and remove the
  others without prompting.
- **Free the main account.** Move all file content out of the main account into its backup accounts,
  choosing destinations with the most free space, so the main account holds as little as possible.
  Refuse if the backups together cannot hold it. Use safe relocation (prefer native transfer, fall
  back to copy-then-remove, never lose data).
- **Balance storage.** When any backup account is nearly full, relocate its largest files to a
  less-full backup account **of the same provider** until the crowded account drops back under a
  safe threshold. Use the same safe relocation rules. (Providers without a meaningful quota are
  exempt as sources of pressure.)
- **Synchronize across providers.** Make the active set of files and the folder structure
  consistent everywhere (see [07-synchronization-rules.md](07-synchronization-rules.md)): push files
  that are missing on a provider, propagate folder structure, honor soft-deletions, resolve
  conflicts, and restore files that have been lost from one provider using a surviving replica on
  another.
- **Clean unmanaged content from backups.** Identify anything in a backup account that sits outside
  the managed area and remove it, so backup accounts hold only pool data. Must support dry-run.

## 6.5 The composite workflow

- **Full sync.** A single capability that runs the healing/optimizing steps in a sensible order to
  bring the whole pool to a healthy state in one shot: assess quotas, empty the main account,
  remove duplicates (automatically by default, interactively on request), make providers consistent,
  and rebalance storage. This is the primary capability for unattended/scheduled use.

## 6.6 Scheduling (deployment mode)

- **Manage a schedule.** In the self-contained deployment mode, provide a way to install, inspect,
  and remove a recurring scheduled run of the full sync workflow using the host operating system's
  native scheduling facility, running periodically without manual intervention.

## 6.7 Testing capability

- **Self-test.** Provide a capability that exercises the system end to end against real accounts to
  validate a build before it is trusted. It must be able to start from a clean slate (optionally
  skipping the destructive reset), run unattended, optionally stop at the first failure, optionally
  run a single scenario, and report clearly which scenarios passed or failed. The scenarios it must
  cover are defined in [08-quality-and-acceptance.md](08-quality-and-acceptance.md). Its destructive
  setup must require explicit confirmation unless explicitly forced.
