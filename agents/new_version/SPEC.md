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
items don't count. On Google Drive, if the single cloud-drives-sync-root exists but is not located in the account's
actual root directory, move it back to the root before proceeding.

---

## logical_files vs. replicas

A **logical_file** is the abstract "file the user has" — provider-agnostic, has a lifecycle
(active, soft-deleted, permanently deleted).

A **replica** is one physical copy on one specific account. A logical_file can have many replicas.
Each replica carries the provider's stable file ID and its content fingerprint.

---

## File identity across providers

A logical_file's canonical identity is its **Google Drive MD5**. Every logical_file must have a
replica on Google Drive; its Google-provided MD5 is the identity that ties all of its replicas
together across providers. **Google Drive is also authoritative for the canonical path** stored in the logical_files table.

- **Same content on the same provider?** → compare provider fingerprints (MD5 on Google, SHA1 on
  OneDrive).
- **Same logical_file across providers?** → match on the Google Drive MD5 recorded in the metadata
  database. Do not use path or (name, size) for cross-provider matching, and never compare a
  non-Google fingerprint against a Google MD5.
- **A replica on another provider not yet linked to any Google Drive MD5** has no established
  identity. To establish it, upload that file to Google Drive, read back the MD5 Google assigns, and
  record it as the logical_file's identity. Only then is the replica considered matched.
- **Path authority:** When collecting metadata, if the same logical_file (by Google Drive MD5) exists at different paths on different providers, the Google Drive replica's location becomes canonical and is stored in logical_files. All other replicas record their actual paths in the replica table and are corrected during sync.

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
  at the correct path: a native shortcut if possible, otherwise a zero-byte **placeholder**. Both count as "the file is accounted for here."
- **Placeholder naming:** A placeholder is named after the original file with the logical_file's Google Drive MD5 encoded and a `.placeholder` extension appended: `filename.ext.md5-<md5>.placeholder` (e.g. `report.pdf.md5-d41d8cd98f00b204e9800998ecf8427e.placeholder`). Because the placeholder itself is zero-byte, this lets the tool recover the original file's name and match it back to its logical_file (by Google Drive MD5) from the placeholder alone.
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
  object** (in its caption/metadata), so the logical_file and its place in the tree can be
  reconstructed from Telegram alone.
- Files are stored in one dedicated managed channel. Multiple channels → stop and ask user.
- The provider ID for an uploaded object is only known after upload, so: upload first, then update
  the object's embedded metadata with the real ID.
- Files larger than Telegram's per-object limit (variable, 2 GB for non-tests and 2 MB for tests) are split into ordered **fragments**,
  tracked individually, reassembled on demand.
- Non-file messages (channel creation notices, membership events, etc.) are ignored.
- **Caption schema:** Each stored object's caption carries a JSON object mirroring the database rows so the logical_file and its place in the tree can be reconstructed from Telegram alone. The `replica_fragment` key is omitted when the object is not fragmented:
  ```json
  {
    "replica": {
      "logical_file_id": "uuid-...",
      "google_drive_md5": "...",
      "path": "/folder/file.ext",
      "name": "file.ext",
      "size": 12345,
      "provider": "telegram",
      "account_id": "+15550109999",
      "native_id": "msg_id_...",
      "native_hash": null,
      "mod_time": 1704067200,
      "status": "active",
      "fragmented": true
    },
    "replica_fragment": {
      "fragment_number": 1,
      "fragments_total": 2,
      "size": 6000,
      "native_fragment_id": "tg_unique_id_..."
    }
  }
  ```
- **Two-step creation:** Because `native_id` and `native_fragment_id` are unknown before upload, upload the object first with a partial caption, then read the resulting message ID and file unique ID from the upload result and rewrite the caption with the complete JSON.

---

## State the tool must track

The tool keeps an **encrypted SQLCipher local database** (password = master password, queryable by the user
outside the tool) called cloud-drives-sync-metadata.db. After any command that changes it, upload a copy to cloud-drives-sync-aux in every 
account. Before any command that needs it, obtain the freshest (by last modified timestamp) copy: local → main account → other
providers in order. If not found anywhere (and this isn't first-time setup config --init), stop with an error.

The database must include tables:

- **logical_files:** stable internal ID, Google Drive MD5 (canonical identity), logical path,
  name, size, mod time, lifecycle state (active / soft-deleted / deleted).
- **replicas:** link to logical_file, provider, account, provider's stable file ID, provider
  fingerprint, path, name, size, mod time, lifecycle state, whether fragmented, owner account, last
  time it was confirmed to still exist.
- **fragments:** link to replica, sequence position, total count, size, provider's fragment ID.
- **logical_folders:** stable internal ID, logical path, name, parent logical_folder, lifecycle
  state (active / soft-deleted / deleted).
- **folder_replicas:** link to logical_folder, provider, account, provider's stable folder ID,
  owner account, last time it was confirmed to still exist. (Google: one replica per logical_folder,
  owned by main and shared. OneDrive: one replica per backup account, each owning its own copy.
  Telegram: none.)

Note: Database is not self referential to avoid circular conflicts. There is no cloud-drives-sync-metadata.db entry in logical_files or replicas.

---

## Security and configuration

- One master password unlocks everything at runtime. Nothing sensitive is ever written in the clear.
- Credentials and tokens live in an **encrypted configuration json** stored on disk as `config.json.enc`
  (AES-256-GCM authenticated encryption; key derived from the master password via Argon2id). A unique,
  cryptographically secure salt is generated once on first run, stored in `config.salt`, and reused for all
  subsequent key derivations.
- The database is also encrypted with the master password (SQLCipher) and must remain directly queryable using
  standard database tooling. The database login user is `owner`.
- Config holds: per-provider application credentials, and per-account: provider, identifier
  (email/phone), main-account flag, durable auth token/refresh token/session.
- **OAuth scopes / auth:** Google requests `https://www.googleapis.com/auth/drive` and
  `https://www.googleapis.com/auth/userinfo.email`; Microsoft requests `files.readwrite.all`, `user.read`, and
  `offline_access`; Telegram authenticates with phone number and verification code. Google/Microsoft flows run a
  local web server to capture the OAuth redirect.
- **Microsoft app registration:** The Azure App Registration must support "Accounts in any organizational
  directory (Any Azure AD directory — Multitenant)" for OneDrive for Business.

*`config.json` (before encryption):*
```json
{
  "google_client": {
    "id": "YOUR_GCP_CLIENT_ID",
    "secret": "YOUR_GCP_CLIENT_SECRET"
  },
  "microsoft_client": {
    "id": "YOUR_AZURE_CLIENT_ID",
    "secret": "YOUR_AZURE_CLIENT_SECRET"
  },
  "telegram_client": {
    "api_id": "YOUR_TELEGRAM_API_ID",
    "api_hash": "YOUR_TELEGRAM_API_HASH"
  },
  "users": [
    {
      "provider": "Google",
      "email": "main.user@gmail.com",
      "is_main": true,
      "refresh_token": "..."
    },
    {
      "provider": "Google",
      "email": "backup1@gmail.com",
      "is_main": false,
      "refresh_token": "..."
    },
    {
      "provider": "Microsoft",
      "email": "user@hotmail.com",
      "refresh_token": "..."
    },
    {
      "provider": "Microsoft",
      "email": "work.backup@company.com",
      "refresh_token": "..."
    },
    {
      "provider": "Telegram",
      "phone": "+1234567890",
      "session_data": "..."
    }
  ]
}
```

---

## Synchronization rules

After a successful sync, all of the following must be true:

1. Every active logical_file exists with identical content on every provider, at the
   same logical path (the path from the logical_files table, which is always Google Drive's canonical path). Each logical_file has exactly one single replica per provider.
2. The folder structure is identical across all providers with a real structure (not Telegram).
3. No active file is missing on any provider.
4. File state (active, soft-deleted, deleted) is the same anywhere.
5. No duplicates remain (based on provider fingerprint).
6. All Google drive folders are owned by the main account, all Google Drive files are owned by a backup account (any).
7. cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted is empty in Google Drive and OneDrive.

**Gaps:** A file present on one provider but absent on another is uploaded, recreating any needed
folders.

**Path conflicts:** If a logical_file exists at different paths on different providers, the Google Drive replica's path is authoritative. All other providers' copies are moved to match Google Drive's path on the next sync (creating folders as needed).

**Content conflicts:** Same path, different content → preserve both: rename the divergent copy with a timestamped suffix (format `_conflict_YYYY-MM-DD_hh-mm-ss`, inserted before the file extension), never silently overwrite.

**Soft deletion:** Moving a file to cloud-drives-sync-root/cloud-drives-sync-aux/soft-deleted propagates that state everywhere (active
copies on other providers are moved to soft-deleted too). Moving a file back makes it active and re-mirrors it everywhere.

**Hard deletion:** Moving a file to cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted removes it from every provider 
and marks its state in the database as hard-deleted. This is the only way to destroy non-duplicated data; it must be deliberate.

**Loss detection and self-healing:** A replica previously recorded but now missing (without having
been hard-deleted) is **lost**. Restore it from a surviving replica, including reassembling from
Telegram fragments when necessary. Retrieve Google Drive MD5 from the surviving/reuploaded replica and record
it as the logical_file's identity if it was previously unknown.

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

With no flags, runs the full workflow in order: sync-unsynced-files → quota check → free-main → sync-providers → balance-storage. Individual flags run only that one operation.

| Flag | What it must accomplish |
|---|---|
| `--share-with-main` | Verify and repair access permissions so all backup accounts have the correct access to the shared structure. |
| `--get-metadata` | Recursively scan every account's managed area and update the local database to match reality: all files, replicas, fragments, folders, shortcuts, and placeholders. **Google Drive is authoritative for paths:** when the same logical_file (matched by Google Drive MD5 or content fingerprint) exists at different paths across providers, the Google Drive replica's path becomes the canonical path in logical_files; all other replicas' paths in the replica table reflect their actual locations, but will be corrected on the next sync. |
| `--quota` | Report used/available space per provider (aggregated), cross-check that usage fits within other providers' capacity. |
| `--free-main` | Move all file content off the main account to the backup accounts with the most free space. Error if backups lack combined capacity. Never delete source before destination is confirmed. |
| `--balance-storage` | When any backup account exceeds ~95% full, move its largest files to the emptiest backup account on the same provider until it drops below ~90%. Same provider only; Telegram (no quota) is exempt as a pressure source. |
| `--sync-providers` | Apply all synchronization rules: fill gaps, resolve conflicts, propagate soft-deletions, restore lost replicas, mirror folder structure. |
| `--sync-unsynced-files` | Move everything in Google Drive backup accounts that sits on Google Drive actual root (no folder) to cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups. |

### `test` — end-to-end self-test

Runs all acceptance scenarios against real accounts. 
Mimics user manual interaction directly with the provider's clouds and logs all actions. Example: `[MANUAL INTERACTION] [main@gmail.com] Create folder 'test-case-id-4-folder' in cloud-drives-sync-root`.
Generates a log at invocation_path/logs/test-YYYYMMDD-HHMMSS.log (or test-YYYYMMDD-HHMMSS-case-{test-case-id}.log).

| Flag | What it must accomplish |
|---|---|
| `--case {test-case-id}` | Run one test case only. Without this flag, run all test cases in order. |
| `--unsafe` | If there is pre-existing data in cloud-drives-sync-root (and subfolders), delete it all before running the test. |
| `--backup` | If there is pre-existing data in cloud-drives-sync-root (and subfolders), rename cloud-drives-sync-root to cloud-drives-sync-root-backup-{timestamp} before running the test. |
| `--with-commit` | Force commits current local state to "test" branch, runs tests, then merges to main ONLY on tests success. The flag accepts a string input that is used as the commit message when provided. If omitted, the tool looks for file `.commitmsg` in the current working directory and uses its content as the commit message. If neither is provided, the tool prompts the user for a commit message. The commit message must be non-empty. If the commit fails, the test stops with an error. On test failure, no merge happens and the working tree is returned to its pre-command state on `main` (uncommitted changes preserved). |

If there is pre-existing data in cloud-drives-sync-root (and subfolders) and neither `--unsafe` nor `--backup` is specified, the test stops with an error.

#### Shared test conventions

- `{rand_str}` means a random string generated at test runtime and stored in memory for later comparison. Unless a test says otherwise, `{rand_str}` is 69 characters long.
- Test case IDs come in two categories: purely numeric IDs (for example `1`, `17`, `25`) and alphanumeric IDs containing the word `soft` (for example `soft1`, `soft2`).
- Numeric test cases are terminating: if one fails, the test command fails.
- `soft` test cases are non-terminating: if one fails, the tool logs a warning, continues running the remaining tests, and does not count that result as an overall test failure.
- When a test says to compare file contents across providers, download the file from each provider and verify the contents match the expected text exactly.
- When a test says a file has the correct Google Drive MD5 or correct provider fingerprints, validate those values both in the provider metadata and in `cloud-drives-sync-metadata.db`.
- Conflict renaming uses the same format as test 21: `_conflict_YYYY-MM-DD_hh-mm-ss` inserted before the file extension.

#### Global validations

Unless a test explicitly overrides these expectations, apply the following validations whenever relevant:

- **Replicated file validation:** The file exists on every account, with identical content and Google Drive MD5.
- **Google Drive file placement:** In Google Drive, the file is owned by a backup account and shared to main (editor).
- **Microsoft OneDrive file placement:** In Microsoft OneDrive, the file is owned by a single backup account and mirrored as a shortcut or placeholder in every other backup account.
- **Telegram file placement:** In Telegram, the file is stored in the managed channel with embedded metadata.
- **File database validation:** In `cloud-drives-sync-metadata.db`, the logical_file row has the correct Google Drive MD5 and the replica rows have the correct provider fingerprints.
- **Replicated folder validation:** The folder exists on every account and provider, except Telegram when the folder is empty.
- **Google Drive folder placement:** On Google Drive, the folder is owned by main and shared to all backup accounts.
- **Microsoft OneDrive folder placement:** On Microsoft OneDrive, each backup account owns its own copy of the folder.
- **Folder database validation:** In `cloud-drives-sync-metadata.db`, the logical_folder row exists and the folder_replica rows exist for every relevant provider.
- **No duplicate sibling names validation:** At any given location, there are no duplicate names: no two files with the same name in the same folder and no two folders with the same name in the same parent folder. Validate this on Google Drive and Telegram only, because Microsoft OneDrive already enforces name uniqueness.

#### Test cases

| test-case-id | Test Name | What to do | Validation |
|---|---|---|---|
| 1 | Clean-slate setup | Run config --init -p {pass} | `cloud-drives-sync-root`, `cloud-drives-sync-root/cloud-drives-sync-aux`, `cloud-drives-sync-root/cloud-drives-sync-aux/cloud-drives-sync-metadata.db`, `cloud-drives-sync-root/cloud-drives-sync-aux/soft-deleted`, `cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted`, and `cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups` exist on every account and are owned by main account on Google Drive. `cloud-drives-sync-metadata.db` is encrypted, queryable with the master password, contains `logical_folder` and `folder_replica` rows for the special folders, and contains no other rows in any other tables (because test always starts with a clean slate). |
| 2 | Create file on main | "manually" create `test-case-id-2.txt` (containing text "test-case-id = 2\n{rand_str}") in `cloud-drives-sync-root` using main account, then run sync. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. |
| 3 | Create file on Google backup | "manually" create `test-case-id-3.txt` (containing text "test-case-id = 3\n{rand_str}") in `cloud-drives-sync-root` using a Google Drive backup account, then run sync. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. |
| 4 | Create folder on main | "manually" create `test-case-id-4-folder` in `cloud-drives-sync-root` using main account, then run sync. | Apply replicated folder validation, Google Drive folder placement, Microsoft OneDrive folder placement, and folder database validation (except Telegram, which has no empty folders). |
| 5 | Create folder on Google backup | "manually" create `test-case-id-5-folder` in `cloud-drives-sync-root` using a Google Drive backup account, then run sync. | Apply replicated folder validation, Google Drive folder placement, Microsoft OneDrive folder placement, and folder database validation (except Telegram, which has no empty folders). |
| 6 | Create file on Microsoft backup | "manually" create `test-case-id-6.txt` (containing text "test-case-id = 6\n{rand_str}") in `cloud-drives-sync-root` using a Microsoft OneDrive backup account, then run sync. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. |
| 7 | Create folder on Microsoft backup | "manually" create `test-case-id-7-folder` in `cloud-drives-sync-root` using a Microsoft OneDrive backup account, then run sync. | Apply replicated folder validation, Google Drive folder placement, Microsoft OneDrive folder placement, and folder database validation (except Telegram, which has no empty folders). |
| 8 | Sync file from Telegram | "manually" upload `test-case-id-8.txt` (containing text "test-case-id = 8\n{rand_str}") to Google Drive main account, then run sync. Then "manually" delete the file from all Google Drive and Microsoft OneDrive accounts, leaving only the Telegram replica. Then run sync again. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. |
| 9 | Sync file from Microsoft | "manually" upload `test-case-id-9.txt` (containing text "test-case-id = 9\n{rand_str}") to Google Drive main account, then run sync. Then "manually" delete the file from all Google Drive and Telegram accounts, leaving only the Microsoft replica. Then run sync again. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. |
| 10 | Move Google Drive files from backups roots | "manually" create test-case-id-10.txt (containing text "test-case-id = 10\n{rand_str}") in the actual root of a Google Drive backup account, then run sync. | `test-case-id-10.txt` exists on every account at `cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups`. Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. |
| 11 | Google Drive nested folders | "manually" create a nested folder structure `test-case-id-11-folder/test-case-id-11-subfolder/test-case-id-11-subsubfolder` in `cloud-drives-sync-root` using main account, then run sync. | Apply replicated folder validation, Google Drive folder placement, Microsoft OneDrive folder placement, and folder database validation for the full nested structure (except Telegram, which has no empty folders). |
| 12 | Microsoft OneDrive nested folders | "manually" create a nested folder structure `test-case-id-12-folder/test-case-id-12-subfolder/test-case-id-12-subsubfolder` in `cloud-drives-sync-root` using a Microsoft OneDrive backup account, then run sync. | Apply replicated folder validation, Google Drive folder placement, Microsoft OneDrive folder placement, and folder database validation for the full nested structure (except Telegram, which has no empty folders). |
| 13 | Google Drive moved file | "manually" create `test-case-id-13.txt` (containing text "test-case-id = 13\n{rand_str}") and a folder `test-case-id-13-folder` in `cloud-drives-sync-root` using main account, then run sync. Then "manually" move the file into the folder and run sync again. | `test-case-id-13.txt` exists on every account at `cloud-drives-sync-root/test-case-id-13-folder/test-case-id-13.txt`. Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement with correct folder path in embedded metadata, and file database validation. |
| 14 | Google Drive files created directly in nested folders | "manually" create nested folder structures `test-case-id-14-folder/test-case-id-14-subfolder-A` and `test-case-id-14-folder/test-case-id-14-subfolder-B` in `cloud-drives-sync-root` using main account, then create files `test-case-id-14-subfolder-A/test-case-id-14-n1.txt` (containing text "test-case-id = 14\n{rand_str}"), `test-case-id-14-subfolder-B/test-case-id-14-n2.txt` (containing text "test-case-id = 14\n{rand_str}") and run sync. | The files exist on every account at their expected paths. Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement with correct folder paths in embedded metadata, and file database validation with correct logical paths. |
| 15 | Google Drive multiple moved files | "manually" create nested folder structures `test-case-id-15-folder/test-case-id-15-subfolder-A/test-case-id-15-subsubfolder-A` and `test-case-id-15-folder/test-case-id-15-subfolder-B/test-case-id-15-subsubfolder-B` and files `test-case-id-15-subfolder-A/test-case-id-15-n1.txt` (containing text "test-case-id = 15\nOrigin: test-case-id-15-subfolder-A\nDestination: test-case-id-15-subsubfolder-A\n{rand_str}"), `test-case-id-15-subsubfolder-A/test-case-id-15-n2.txt` (containing text "test-case-id = 15\nOrigin: test-case-id-15-subsubfolder-A\nDestination: test-case-id-15-subfolder-B\n{rand_str}"), and `test-case-id-15-subfolder-B/test-case-id-15-n3.txt` (containing text "test-case-id = 15\nOrigin: test-case-id-15-subfolder-B\nDestination: test-case-id-15-subfolder-A\n{rand_str}") in `cloud-drives-sync-root` using main account, then run sync. Then move the files to their destinations and run sync again. | The files exist on every account at their new paths. Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement with correct folder paths in embedded metadata, and file database validation. |
| 16 | Google Drive soft-delete and restore | "manually" create `test-case-id-16.txt` (containing text "test-case-id = 16\n{rand_str}") in `cloud-drives-sync-root` using main account, then run sync. Then "manually" move the file to `cloud-drives-sync-root/cloud-drives-sync-aux/soft-deleted` and run sync again. Then move it back to `cloud-drives-sync-root` and run sync again. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement with correct folder path in embedded metadata, and file database validation. The logical_file lifecycle state is `active` after restoration. |
| 17 | Google Drive hard-delete | "manually" create `test-case-id-17.txt` (containing text "test-case-id = 17\n{rand_str}") in `cloud-drives-sync-root` using main account, then run sync. Then "manually" move the file to `cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted` and run sync again. | The file does not exist on any account. In `cloud-drives-sync-metadata.db`, the logical_file row has lifecycle state `hard-deleted` and the replica rows have lifecycle state `hard-deleted`. |
| 18 | Telegram fragmentation and defragmentation restore | "manually" create `test-case-id-18.txt` (containing text "test-case-id = 18\n{rand_str}") in `cloud-drives-sync-root` using main account, where `{rand_str}` is a 2.5 MB random string. Then run sync. Then "manually" delete the file from all Google Drive and Microsoft OneDrive accounts, leaving only the Telegram fragmented replica. Then run sync again. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. The Google Drive and Microsoft OneDrive replicas are not fragmented. The Telegram replica is marked as fragmented, and the fragments table has the correct entries for the Telegram fragments. |
| 19 | Idempotent sync | "manually" create `test-case-id-19.txt` (containing text "test-case-id = 19") and run sync. Retrieve database hash. Run sync again. Retrieve database hash again. | The database hash is identical before and after the second sync. No changes were made to any files or folders on any account. |
| 20 | Quota check | Run sync --quota | The reported used and available space per provider is consistent with the actual usage on each account and the reported usage in `cloud-drives-sync-metadata.db`. No provider exceeds its quota limits. |
| 21 | Divergent content at the same logical path | "manually" create `test-case-id-21.txt` (containing text "test-case-id = 21\nUploaded to provider: Google Drive") in `cloud-drives-sync-root` using main account, then "manually" create `test-case-id-21.txt` (containing text "test-case-id = 21\nUploaded to provider: Microsoft OneDrive") in `cloud-drives-sync-root` using a Microsoft OneDrive backup account, then run sync. | Both versions of `test-case-id-21.txt` exist on every account, but one of them has been renamed using the conflict format. In `cloud-drives-sync-metadata.db`, there are two logical_file rows with different Google Drive MD5s and replica rows with the correct provider fingerprints. |
| 22 | Sync resumed after interruption | "manually" create `test-case-id-22.txt` (containing text "test-case-id = 22") in `cloud-drives-sync-root` using main account, then run sync. Interrupt the sync process (e.g., kill the process) during the transfer of the file to a backup account. Restart the sync process. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. No duplicates were created during the interrupted sync, and no data was lost. |
| 23 | MS Placeholders | Check that there are at least 2 Microsoft OneDrive backup accounts. "Manually" create `test-case-id-23.txt` (containing text "test-case-id = 23") in `cloud-drives-sync-root` using a Microsoft OneDrive backup account, then run sync. Then run sync again. | Apply replicated file validation, Google Drive file placement, Microsoft OneDrive file placement, Telegram file placement, and file database validation. Shortcuts and placeholders are not duplicated across accounts or propagated to Google Drive or Telegram as if they were real files. |
| 24 | Google Drive duplicate sibling folders merge | "manually" create folder `test-case-id-24-grandparent` in `cloud-drives-sync-root` using main account. Inside it, create two sibling folders with the same name `test-case-id-24-parent`. In one `test-case-id-24-parent` folder create file `test-case-id-24-parent-A.txt`; in the other create file `test-case-id-24-parent-B.txt`. Then, inside each `test-case-id-24-parent` folder create a folder named `test-case-id-24-child`. In one `test-case-id-24-child` folder create file `test-case-id-24-child-A.txt`; in the other create file `test-case-id-24-child-B.txt`. Then run sync. | On every provider and account, there is exactly one folder at path `cloud-drives-sync-root/test-case-id-24-grandparent/test-case-id-24-parent` containing both files `test-case-id-24-parent-A.txt` and `test-case-id-24-parent-B.txt`. Inside it, there is exactly one folder at path `cloud-drives-sync-root/test-case-id-24-grandparent/test-case-id-24-parent/test-case-id-24-child` containing both files `test-case-id-24-child-A.txt` and `test-case-id-24-child-B.txt`. The merge only applies to folders with the same name under the same parent; folders with the same name under different parents are not merged. In `cloud-drives-sync-metadata.db`, the logical_folder and folder_replica rows represent the merged logical paths without duplicate sibling folders. |
| 25 | Cross-provider duplicate nested folders with file conflicts | "manually" create folder `test-case-id-25-outer` in `cloud-drives-sync-root` using Google Drive main account, then create folder `test-case-id-25-inner` inside it. Create file `test-case-id-25-outer.txt` inside `test-case-id-25-outer` with content `Uploaded to provider: Google Drive`, and file `test-case-id-25-inner.txt` inside `test-case-id-25-inner` with content `Uploaded to provider: Google Drive`. Then, using a Microsoft OneDrive backup account, create folder `test-case-id-25-outer` in `cloud-drives-sync-root` and folder `test-case-id-25-inner` inside it. Create file `test-case-id-25-outer.txt` inside `test-case-id-25-outer` with content `Uploaded to provider: Microsoft OneDrive`, and file `test-case-id-25-inner.txt` inside `test-case-id-25-inner` with content `Uploaded to provider: Microsoft OneDrive`. Then run sync. | On every provider and account, there is exactly one folder at path `cloud-drives-sync-root/test-case-id-25-outer` and exactly one folder at path `cloud-drives-sync-root/test-case-id-25-outer/test-case-id-25-inner`. Across those merged folders, all four files exist after sync: both versions of `test-case-id-25-outer.txt` are present in `test-case-id-25-outer`, and both versions of `test-case-id-25-inner.txt` are present in `test-case-id-25-outer/test-case-id-25-inner`, with one version of each conflicting filename renamed using the conflict format. File contents remain provider-specific and are not overwritten. In `cloud-drives-sync-metadata.db`, the logical_folder and folder_replica rows represent the merged folder paths, and there are distinct logical_file rows for each conflicting content variant with correct provider fingerprints. |
| soft1 | Google Drive Transfer Ownership | "manually" create `test-case-id-soft1.txt` (containing text "test-case-id = soft1") in `cloud-drives-sync-root` using main account, then run sync. | File was tranfered using "Transfer Ownership" flow, not download-reupload-delete. |
| soft2 | Microsoft OneDrive Real Shortcut | "manually" create `test-case-id-soft2.txt` (containing text "test-case-id = soft2") in `cloud-drives-sync-root` using a Microsoft OneDrive backup account, then run sync. | File was mirrored as a real shortcut in other OneDrive backup accounts, not as a placeholder. |

---

## Deployment build (auto)

A separate build variant (using //go:build auto) embeds the encrypted config (config.json) directly in the binary (no init step needed at
the target machine — only the master password at auto set or sync). 
This variant only exposes `sync -p {pass_from_env_var}`, `config --auto --set` and `config --auto --disable`. All other commands or flag combinations are unavailable, even --help.
sync command compiled with this build variant produces no logs and no detailed output, only exit codes. It is intended for scheduled runs on a headless server, not for interactive use.

**Schedule interval and triggers:** The recurring run fires every 8 hours.
- **Windows (Task Scheduler):** creates a task named `cloud-drives-sync` that triggers on logon and repeats every 8 hours; `--set` also runs it immediately once. `--disable` deletes the task.
- **Linux (systemd user timer):** writes service and timer units to `~/.config/systemd/user/`, then enables and starts the timer; the timer fires 5 minutes after boot and then every 8 hours. `--disable` stops and disables the timer and removes the unit files.

---

## Constraints

- Never read, list, modify, or delete anything outside the cloud-drives-sync-root. The only exception is files in Google Drive backup account's actual root (no folder) — those are moved to cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups.
- When doing download/reupload/delete, never destroy an original before its replacement is confirmed intact (same filesize).
- Concurrent independent reads across accounts are fine; all writes to the local database are serialized.
- All file transfers are streamed (no whole-file in-memory buffering).
- Never empty trash or recycle bin on any provider. Files moved to cloud-drives-sync-root/cloud-drives-sync-aux/hard-deleted are moved to trash/recycle bin.
- All cloud/api calls retry with backoff on transient errors.
