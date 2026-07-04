# Cloud Drives Sync

`cloud-drives-sync` is a robust command-line tool designed to manage and synchronize files across multiple cloud storage providers, including Google Drive, Microsoft OneDrive for Business, and Telegram.

The tool uses a single "main account" (Google Drive) as the primary synchronization target, with one or more "backup accounts" (from any supported provider) used to expand storage and provide data redundancy. 

## Features

- **Multi-Provider Sync:** Seamlessly sync files between Google Drive, Microsoft OneDrive for Business, and Telegram.
- **Storage Expansion & Balancing:** Offload files from your main account to backup accounts and balance storage usage automatically.
- **Deduplication:** Find and remove duplicate files natively within the command line.
- **Encrypted Local Metadata:** Uses SQLCipher to maintain a local `cloud-drives-sync-metadata.db` database to quickly query and track the state of your cloud files.
- **End-to-End Security:** API keys and refresh tokens are stored in an AES-256 GCM encrypted `config.json.enc` file protected by your master password.
- **Telegram Large File Support:** Automatically splits files larger than Telegram's limits (2 GB) into fragments and recombines them transparently.

## Installation

```bash
# Standard build (all commands available)
go build -o cloud-drives-sync.exe .

# Auto build (embeds config.json.enc and config.salt into the binary)
# Only `sync`, `config --auto`, and help are available.
# No init step needed — just provide the master password at runtime.
go build -tags auto -o cloud-drives-sync-auto.exe .
```

## Quick Start

1. **Initialize the setup** (prompts for master password, API credentials, and adds your main account):
   ```bash
   cloud-drives-sync config --init
   ```
2. **Add backup accounts**:
   ```bash
   cloud-drives-sync config --add-account
   ```
3. **Scan your providers** to build the initial local metadata:
   ```bash
   cloud-drives-sync sync --get-metadata
   ```
4. **Run a full synchronization workflow** (moves unsynced backup-root files into the fence, checks quotas, frees the main account, deduplicates, syncs providers, and balances storage):
   ```bash
   cloud-drives-sync sync
   ```

## Global Flags

- `-p, --password string` : Provide the master password non-interactively.
- `-s, --safe` : Dry run mode for `sync` - perform read-only actions and log what *would* be changed without modifying cloud files.
- `-h, --help` : Show help for any command.

## Commands

The tool exposes four top-level commands: `config`, `sync`, `test`, and `help`. Each action is selected with a flag (exactly one action flag per invocation).

### `config` — manage configuration and accounts

| Flag | Description | Standard | Auto |
|---|---|:---:|:---:|
| `--init` | First-time setup, or update credentials / main account | ✓ | ✗ |
| `--add-account` | Add a backup account | ✓ | ✗ |
| `--remove-account` | Remove an account from the local configuration | ✓ | ✗ |
| `--check-tokens` | Report which stored credentials still work | ✓ | ✗ |
| `--reauth` (`--all`) | Re-authenticate broken (or all) accounts | ✓ | ✗ |
| `--auto` (`--set` / `--disable`) | Install/remove the recurring scheduled sync | ✗ | ✓ |

### `sync` — operate and maintain the pool

With no flag, runs the full workflow: `sync-unsynced-files → quota → free-main → remove-duplicates → sync-providers → balance-storage`.

| Flag | Description | Standard | Auto |
|---|---|:---:|:---:|
| *(none)* | Run the full synchronization workflow | ✓ | ✓ |
| `--share-with-main` | Verify and repair share permissions with main accounts | ✓ | ✗ |
| `--get-metadata` | Scan all providers and update the local metadata database | ✓ | ✗ |
| `--quota` | Report used/available quota per provider | ✓ | ✗ |
| `--check-for-duplicates` | Report duplicate files within each provider | ✓ | ✗ |
| `--remove-duplicates` | Interactively remove duplicate files | ✓ | ✗ |
| `--remove-duplicates-unsafe` | Automatically remove duplicates (keeps the oldest) | ✓ | ✗ |
| `--free-main` | Move all file content off the main account to backups | ✓ | ✗ |
| `--balance-storage` | Balance storage usage across backup accounts | ✓ | ✗ |
| `--sync-providers` | Synchronize files across all providers | ✓ | ✗ |
| `--sync-unsynced-files` | Move Google backup-root files into `cloud-drives-sync-aux/unsynced-from-backups` | ✓ | ✗ |

### `test` — end-to-end self-test

| Flag | Description |
|---|---|
| `--case {id}` | Run one test case only |
| `--unsafe` | Delete pre-existing data in the sync root before running |
| `--backup` | Rename pre-existing sync root to a timestamped backup before running |
| `--with-commit [msg]` | Commit to a test branch, run tests, and merge to main only on success |

### Auto Command

The `auto` command is only available in auto builds (`go build -tags auto`). It creates or removes a scheduled task (Windows) or systemd timer (Linux) that runs `sync` every 8 hours.

```bash
# Create the scheduled sync (runs immediately + every 8 hours)
cloud-drives-sync-auto auto --set -p <master-password>

# Check current status
cloud-drives-sync-auto auto

# Remove the scheduled sync
cloud-drives-sync-auto auto --disable
```

Flags:
- `--set` : Create the scheduled task/service (requires `-p`)
- `--disable`, `-d` : Remove the scheduled task/service
- No flags : Show whether the schedule is currently installed

## Project Architecture & Data

- **Sync Folder:** The tool only interacts with files inside a specific folder structure (`cloud-drives-sync-root` and `cloud-drives-sync-aux/{soft-deleted,hard-deleted,unsynced-from-backups}`). It will never modify files outside of these directories.
- **Database:** Local metadata is stored in `cloud-drives-sync-metadata.db`. You can view `DATABASE_ACCESS.md` for information on how to query it manually using Python, Go, or DB Browser for SQLCipher.
- **Testing:** The `test` command runs a suite of full end-to-end integration tests mimicking complex file movements, fragmentation, soft deletions, and more. See `TEST.md` for instructions on the test suite loop.
- **Auto Build:** When built with `-tags auto`, the binary embeds `config.json.enc` and `config.salt` at compile time. This creates a self-contained binary that requires no `init` step — only the master password at runtime. Available commands are restricted to `sync`, `config --auto`, and `help`.