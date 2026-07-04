# cloud-drives-sync — What to Build

A command-line tool that turns several personal cloud storage accounts into one redundant,
load-balanced, self-healing pool, operated entirely from the terminal.

---

## Accounts and roles

The user owns accounts across three providers: **Google Drive**, **Microsoft OneDrive**, and **Telegram**. One Google account is the **main account** (source of truth,
actively kept empty). All other accounts are **backup accounts** (any provider, any number).

---

## The managed area ("the fence")

Each account has exactly one cloud-drives-sync-root folder. The tool only ever touches files and folders
inside it. Everything else in the user's accounts is invisible and must never be modified.

Inside the root there is also a reserved **cloud-drives-sync-aux** folder for the tool's own housekeeping
(soft-deleted items, the replicated database). The rest of the root is the user's active library.

**Pre-flight:** Before any cloud work, verify exactly one cloud-drives-sync-root exists per account. Zero →
create it (or, if Google Drive, share it from main). More than one → stop and ask the user to resolve. Trashed/recycled
items don't count.

---

## Logical files vs. replicas

A **logical file** is the abstract "file the user has" — provider-agnostic, has a lifecycle
(active, soft-deleted, permanently deleted).

A **replica** is one physical copy on one specific account. A logical file can have many replicas.
Each replica carries the provider's stable file ID and its content fingerprint.

---

## File identity across providers

A logical file's canonical identity is its **Google Drive MD5**. Every logical file must have a
replica on Google Drive; its Google-provided MD5 is the identity that ties all of its replicas
together across providers.

- **Same content on the same provider?** → compare provider fingerprints (MD5 on Google, SHA1 on
  OneDrive).
- **Same logical file across providers?** → match on the Google Drive MD5 recorded in the metadata
  database. Do not use path or (name, size) for cross-provider matching, and never compare a
  non-Google fingerprint against a Google MD5.
- **A replica on another provider not yet linked to any Google Drive MD5** has no established
  identity. To establish it, upload that file to Google Drive, read back the MD5 Google assigns, and
  record it as the logical file's identity. Only then is the replica considered matched.

---

## Provider-specific rules

### Google Drive
- The main account owns the folder structure. Backup accounts own only files, not folders.
- The cloud-drives-sync-root and all subfolders are **shared** with all Google backup accounts (editor access).
- To move a file between accounts, prefer native ownership transfer; fall back to copy-then-remove
  (delete source only after the copy is confirmed intact, matching md5).

### Microsoft OneDrive
- Each backup account owns its **own** cloud-drives-sync-root and all its sub-folders.
- Files are distributed across accounts. An account that doesn't own a file must have a **reference**
  at the correct path: a native shortcut if possible, otherwise a zero-byte **placeholder** with original file's name plus ".placeholder" extension. Both count as "the file is accounted for here."
- The complete folder tree (including empty folders) must be mirrored across every Microsoft backup
  account. No missing entries, no redundant real copies.
- File moves: copy-then-remove (never destroy source first).

**Why the difference:** Google Drive allows a file owned by Account A to live inside a folder owned
by Account B (shared folder model). OneDrive's permission model ties folder ownership to content
ownership — whoever owns a folder owns all its contents by definition. This architectural difference
forces different strategies: Google can centralize structure while spreading file ownership; OneDrive
requires each account to own its own complete structure.

### Telegram
- No folder structure. All identity and path information must be embedded **within each stored
  object** (in its caption/metadata), so the logical file and its place in the tree can be
  reconstructed from Telegram alone.
- Files are stored in one dedicated managed channel. Multiple channels → stop and ask user.
- The provider ID for an uploaded object is only known after upload, so: upload first, then update
  the object's embedded metadata with the real ID.
- Files larger than Telegram's per-object limit (2 GB) are split into ordered **fragments**,
  tracked individually, reassembled on demand.
- Non-file messages (channel creation notices, membership events, etc.) are ignored.

---

## State the tool must track

The tool keeps an **encrypted SQLCipher local database** (password = master password, queryable by the user
outside the tool) called cloud-drives-sync-metadata.db. After any command that changes it, upload a copy to cloud-drives-sync-aux in every 
account. Before any command that needs it, obtain the freshest (by last modified timestamp) copy: local → main account → other
providers in order. If not found anywhere (and this isn't first-time setup config --init), stop with an error.

The database must record:

- **Per logical file:** stable internal ID, Google Drive MD5 (canonical identity), logical path,
  name, size, mod time, lifecycle state (active / soft-deleted / deleted).
- **Per replica:** link to logical file, provider, account, provider's stable file ID, provider
  fingerprint, path, name, size, mod time, lifecycle state, whether fragmented, owner account, last
  time it was confirmed to still exist.
- **Per fragment:** link to replica, sequence position, total count, size, provider's fragment ID.
- **Per logical folder:** stable internal ID, logical path, name, parent logical folder, lifecycle
  state (active / soft-deleted / deleted).
- **Per folder replica:** link to logical folder, provider, account, provider's stable folder ID,
  owner account, last time it was confirmed to still exist. (Google: one replica per logical folder,
  owned by main and shared. OneDrive: one replica per backup account, each owning its own copy.
  Telegram: none.)

---

## Security and configuration

- One master password unlocks everything at runtime. Nothing sensitive is ever written in the clear.
- Credentials and tokens live in an **encrypted configuration file** (strong authenticated
  encryption, key derived from master password via a slow memory-hard KDF, unique salt generated
  once and reused).
- The database is also encrypted with the master password and must remain directly queryable using
  standard database tooling.
- Config holds: per-provider application credentials, and per-account: provider, identifier
  (email/phone), main-account flag, durable auth token/refresh token/session.

---

## Synchronization rules

After a successful sync, all of the following must be true:

1. Every active logical file exists with identical content on every provider, at the
   same logical path. Each logical file has exactly one single replica per provider.
2. The folder structure is identical across all providers with a real structure (not Telegram).
3. No active file is missing on any provider.
4. File state (active, soft-deleted, deleted) is the same anywhere.
5. No duplicates remain (based on provider fingerprint).
6. All Google drive folders are owned by the main account, all Google Drive files are owned by a backup account (any).
7. cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted is empty in Google Drive and OneDrive.

**Gaps:** A file present on one provider but absent on another is uploaded, recreating any needed
folders.

**Conflicts:** Same path, different content → preserve both: rename the divergent copy with a
timestamped suffix, never silently overwrite.

**Soft deletion:** Moving a file to cloud-drives-sync-root/cloud-drives-sync-aux/soft-deleted propagates that state everywhere (active
copies on other providers are moved to soft-deleted too). Moving a file back makes it active and re-mirrors it everywhere.

**Hard deletion:** Moving a file to cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted removes it from every provider 
and marks its state in the database as hard-deleted. This is the only way to destroy non-duplicated data; it must be deliberate.

**Loss detection and self-healing:** A replica previously recorded but now missing (without having
been hard-deleted) is **lost**. Restore it from a surviving replica, including reassembling from
Telegram fragments when necessary. Retrieve Google Drive MD5 from the surviving/reuploaded replica and record
it as the logical file's identity if it was previously unknown.

**Idempotence:** Re-running a completed sync makes no changes. Resuming an interrupted sync
completes remaining work without duplicating already-done work.

---

## Commands

There are four top-level commands: `config`, `sync`, `test` and `help`. Exit 0 on success, non-zero on error.
Exactly one action flag must be provided per invocation (action flags are mutually exclusive within each command).

### Global flags

| Flag | What it does | Supported by commands |
|---|---|---|
| `-p`, `--password` | Provide the master password non-interactively for scripting. If omitted, the tool prompts for it. | `config`, `sync`, `test` |
| `-s`, `--safe` | Dry-run mode: no writes, deletes, or permission changes are sent to the cloud. The tool prints exactly what it *would* do instead (e.g. `[DRY RUN] [backup@gmail.com] DELETE GDrive file 'duplicate.txt' (LogicalFileID: xyz)`). Local reads and database reads are still allowed. | `sync` | 
| `-h`, `--help` | Show help for the command. | all |

### Flags specific to `config --init`

| Flag | What it does |
|---|---|
| `--json` | Pass all client credentials as a JSON string instead of being prompted for them interactively. |
| `--getjson` | After collecting client credentials, print the equivalent JSON string to stdout for reuse in scripting. |

### `config` — manage configuration and accounts

| Flag | What it must accomplish |
|---|---|
| `--init` | First-time setup: collect master password, KDF salt, provider app credentials, authenticate the main account, ensure its cloud-drives-sync-root exists. Re-running updates credentials or main account. Accepts all credentials as structured input; can emit equivalent structured output for scripting. |
| `--add-account` | Authorize and register a backup account. Refuse if no main account. Wire it into the pool (share on Google, create cloud-drives-sync-root on OneDrive). |
| `--remove-account` | Remove an account from config (local only, no cloud deletion). Main account removable only when no backups depend on it. |
| `--check-tokens` | Report which stored credentials still work and which have expired/been revoked. |
| `--reauth` | Fix broken credentials (default: only broken ones; `--all`: every account). Must confirm re-authenticated identity matches the original. |
| `--auto` *(deployment build only)* | Install/remove/status a recurring OS-native scheduled run of `sync` (Windows: Task Scheduler; Linux: systemd user timer). Controlled via:<ul><li>`--set`: Must be used with --password, which is stored as environment variable with random name and retrieved by task/service when running sync (env var random name hardocded in task/service definition)</li><li>`--disable`: Deletes environment variable and task/service</li></ul> |



### `sync` — operate and maintain the pool

With no flags, runs the full workflow in order: sync-unsynced-files → quota check → free-main → remove-duplicates-unsafe
(or interactive with `--safe`) → sync-providers → balance-storage. Individual flags run only that
one operation.

| Flag | What it must accomplish |
|---|---|
| `--share-with-main` | Verify and repair access permissions so all backup accounts have the correct access to the shared structure. |
| `--get-metadata` | Recursively scan every account's managed area and update the local database to match reality: all files, replicas, fragments, folders, shortcuts, and placeholders. |
| `--quota` | Report used/available space per provider (aggregated), cross-check that usage fits within other providers' capacity. |
| `--check-for-duplicates` | Refresh metadata, then report byte-identical files within the same provider, grouped. |
| `--remove-duplicates` | Interactive: for each duplicate set, let the user choose which to delete. |
| `--remove-duplicates-unsafe` | Automatic: keep the oldest copy per set, delete the rest without prompting. |
| `--free-main` | Move all file content off the main account to the backup accounts with the most free space. Error if backups lack combined capacity. Never delete source before destination is confirmed. |
| `--balance-storage` | When any backup account exceeds ~95% full, move its largest files to the emptiest backup account on the same provider until it drops below ~90%. Same provider only; Telegram (no quota) is exempt as a pressure source. |
| `--sync-providers` | Apply all synchronization rules: fill gaps, resolve conflicts, propagate soft-deletions, restore lost replicas, mirror folder structure. |
| `--sync-unsynced-files` | Move everything in Google Drive backup accounts that sits on Google Drive actual root (no folder) to cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups. |

### `test` — end-to-end self-test

Runs all acceptance scenarios against real accounts. 
Mimics user interaction directly with the provider's clouds and logs all actions. Example: `[MANUAL INTERACTION] [main@gmail.com] Create folder 'test-folder-1' in cloud-drives-sync-root`.
Generates a log at invocation_path/logs/test-YYYYMMDD-HHMMSS.log (or test-YYYYMMDD-HHMMSS-case-{test-case-id}.log).

| Flag | What it must accomplish |
|---|---|
| `--case {test-case-id}` | Run one test case only. |
| `--unsafe` | If there is pre-existing data in cloud-drives-sync-root (and subfolders), delete it all before running the test. |
| `--backup` | If there is pre-existing data in cloud-drives-sync-root (and subfolders), rename cloud-drives-sync-root to cloud-drives-sync-root-backup-{timestamp} before running the test. |
| `--with-commit` | Force commits current local state to "test" branch, runs tests, then merges to main ONLY on tests success. |

If there is pre-existing data in cloud-drives-sync-root (and subfolders) and neither `--unsafe` nor `--backup` is specified, the test stops with an error.

#### Test cases

| test-case-id | Test Name | What to do | Validation |
|---|---|---|---|
| 1 | Clean-slate setup | Run config --init -p {pass} | cloud-drives-sync-root, cloud-drives-sync-root/cloud-drives-sync-aux, cloud-drives-sync-root/cloud-drives-sync-aux/cloud-drives-sync-metadata.db, cloud-drives-sync-root/cloud-drives-sync-aux/soft-deleted, cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted, cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups exist on every account and are owned by main account on Google Drive Main account. |
| 2 | Create file on main | "manually" create test-case-id-2.txt (containing text "test-case-id = 2\n{rand_str}" where rand_str is a 69-character random string generated at test runtime, stored in memory for later comparison) in cloud-drives-sync-root using main account, then run sync. | test-case-id-2.txt exists on every account, with identical content (download from each source and check) and Google Drive MD5. In Google Drive, the file is owned by a backup account and shared to main (editor). In Microsoft OneDrive, the file is owned by a single backup account and mirrored as a shortcut or placeholder in every other backup account. In Telegram, the file is stored in the managed channel with embedded metadata. |
| 3 | Create file on Google backup | "manually" create test-case-id-3.txt (containing text "test-case-id = 3\n{rand_str}" where rand_str is a 69-character random string generated at test runtime, stored in memory for later comparison) in cloud-drives-sync-root using a Google Drive backup account, then run sync. | test-case-id-3.txt exists on every account, with identical content (download from each source and check) and Google Drive MD5. In Google Drive, the file is owned by a backup account and shared to main (editor). In Microsoft OneDrive, the file is owned by a single backup account and mirrored as a shortcut or placeholder in every other backup account. In Telegram, the file is stored in the managed channel with embedded metadata. |
| 4 | Create folder on main | "manually" create test-case-id-4-folder in cloud-drives-sync-root using main account, then run sync. | test-case-id-4-folder exists on every account and provider (except Telegram, which has no empty folders). On Google Drive, the folder is owned by main and shared to all backup accounts. On Microsoft OneDrive, each backup account owns its own copy of the folder. |
| 5 | Create folder on Google backup | "manually" create test-case-id-5-folder in cloud-drives-sync-root using a Google Drive backup account, then run sync. | test-case-id-5-folder exists on every account and provider (except Telegram, which has no empty folders). On Google Drive, the folder is owned by main and shared to all backup accounts. On Microsoft OneDrive, each backup account owns its own copy of the folder. |
| 6 | Create file on Microsoft backup | "manually" create test-case-id-6.txt (containing text "test-case-id = 6\n{rand_str}" where rand_str is a 69-character random string generated at test runtime, stored in memory for later comparison) in cloud-drives-sync-root using a Microsoft OneDrive backup account, then run sync. | test-case-id-6.txt exists on every account, with identical content (download from each source and check) and Google Drive MD5. In Google Drive, the file is owned by a backup account and shared to main (editor). In Microsoft OneDrive, the file is owned by a single backup account and mirrored as a shortcut or placeholder in every other backup account. In Telegram, the file is stored in the managed channel with embedded metadata. |
| 7 | 

<!--
## Acceptance scenarios (self-test must cover all)

1. Clean-slate setup produces a well-formed, empty pool.
2. A file on the main account is moved off it and mirrored everywhere.
3. Files created directly on backup accounts of different providers are synced to all.
4. A large file (streaming) is uploaded and synced intact.
5. Creating folders and moving files into them propagates structure and locations everywhere.
6. Moving files to cloud-drives-sync-root/cloud-drives-sync-aux/soft-deleted removes them from active sync consistently everywhere.
7. Deeply nested folder structures are mirrored correctly.
8. Restoring a soft-deleted file re-mirrors it as active everywhere.
9. Hard-deleting soft-deleted files removes them from every provider and from state permanently.
10. Reported quota per provider is internally consistent.
11. A file exceeding a provider's per-object limit is stored as ordered fragments and is fully reconstructable.
12. Files deleted from some providers (simulated loss) are restored by sync using a surviving replica, including reassembly from Telegram fragments.
13. Ownership/storage relocation between accounts uses native transfer when possible, copy-then-remove otherwise, never loses the file.
14. Byte-identical copies within a provider are detected and removed by both the interactive and automatic paths.
15. Divergent content at the same logical path is preserved as both versions with the incoming copy renamed; nothing is silently overwritten.
16. Re-running sync on a consistent pool makes no changes.
17. Sync resumed after interruption completes without creating duplicates or losing data.
-->
---

## Deployment build (auto)

A separate build variant (using //go:build auto) embeds the encrypted config directly in the binary (no init step needed at
the target machine — only the master password at setup). 
This variant only exposes `sync -p {pass_from_env_var}`, `config --auto --set` and `config --auto --disable`. All other commands or flag combinations are unavailable, even --help.
sync command compiled with this build variant produces no logs and no detailed output, only exit codes. It is intended for scheduled runs on a headless server, not for interactive use.

---

## Constraints

- Never read, list, modify, or delete anything outside the cloud-drives-sync-root. The only exception is files in Google Drive backup account's actual root (no folder) — those are moved to cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups.
- When doing download/reupload/delete, never destroy an original before its replacement is confirmed intact (same filesize).
- Concurrent independent reads across accounts are fine; all writes to the local database are serialized.
- All file transfers are streamed (no whole-file in-memory buffering).
- All cloud/api calls retry with backoff on transient errors.
