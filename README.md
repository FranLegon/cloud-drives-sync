# Cloud Drives Sync

`cloud-drives-sync` is a robust command-line tool designed to manage and synchronize files across multiple cloud storage providers, including Google Drive, Microsoft OneDrive for Business, and Telegram.

The tool uses a single "main account" (Google Drive) as the primary synchronization target, with one or more "backup accounts" (from any supported provider) used to expand storage and provide data redundancy. 

## Features

- **Multi-Provider Sync:** Seamlessly sync files between Google Drive, Microsoft OneDrive for Business, and Telegram.
- **Storage Expansion & Balancing:** Offload files from your main account to backup accounts and balance storage usage automatically.
- **Deduplication:** Find and remove duplicate files natively within the command line.
- **Encrypted Local Metadata:** Uses SQLCipher to maintain a local `metadata.db` database to quickly query and track the state of your cloud files.
- **End-to-End Security:** API keys and refresh tokens are stored in an AES-256 GCM encrypted `config.json.enc` file protected by your master password.
- **Telegram Large File Support:** Automatically splits files larger than Telegram's limits (2 GB) into fragments and recombines them transparently.

## Installation

```bash
# Standard build (all commands available)
go build -o cloud-drives-sync.exe .

# Auto build (embeds config.json.enc and config.salt into the binary)
# Only auto, sync, and help commands are available.
# No init step needed — just provide the master password at runtime.
go build -tags auto -o cloud-drives-sync-auto.exe .
```

## Quick Start

1. **Initialize the setup** (prompts for master password, API credentials, and adds your main account):
   ```bash
   cloud-drives-sync init
   ```
2. **Add backup accounts**:
   ```bash
   cloud-drives-sync add-account
   ```
3. **Scan your providers** to build the initial local metadata:
   ```bash
   cloud-drives-sync get-metadata
   ```
4. **Run a full synchronization workflow** (checks quotas, frees main account, deduplicates, and balances storage):
   ```bash
   cloud-drives-sync sync
   ```

## Global Flags

- `-p, --password string` : Provide the master password non-interactively.
- `-s, --safe` : Dry run mode - perform read-only actions and log what *would* be changed without modifying cloud files.
- `-h, --help` : Show help for any command.

## Available Commands

| Command | Description | Standard | Auto |
|---|---|:---:|:---:|
| `add-account` | Add a backup account for an existing provider | ✓ | ✗ |
| `auto` | Manage automatic scheduled synchronization (scheduled task / systemd timer) | ✗ | ✓ |
| `balance-storage` | Balance storage usage across backup accounts | ✓ | ✗ |
| `check-for-duplicates` | Check for duplicate files within each provider | ✓ | ✗ |
| `check-tokens` | Validate all authentication tokens | ✓ | ✗ |
| `delete-unsynced-files` | Delete files in backup accounts that are not in the sync folder | ✓ | ✗ |
| `free-main` | Transfer all files from the main account to backup accounts | ✓ | ✗ |
| `get-metadata` | Scan all cloud providers and update the local metadata database | ✓ | ✗ |
| `init` | Initialize the application or add a main account | ✓ | ✗ |
| `quota` | Calculate and print total used and available quota for each provider | ✓ | ✗ |
| `reauth` | Re-authenticate cloud provider accounts | ✓ | ✗ |
| `remove-account` | Remove a backup account or an entire provider from the configuration | ✓ | ✗ |
| `remove-duplicates` | Interactively remove duplicate files | ✓ | ✗ |
| `remove-duplicates-unsafe` | Automatically remove duplicate files (keeps the oldest) | ✓ | ✗ |
| `share-with-main` | Verify and repair share permissions with main accounts | ✓ | ✗ |
| `sync` | Run the full synchronization workflow | ✓ | ✓ |
| `sync-providers` | Synchronize files across all cloud providers | ✓ | ✗ |
| `test` | Run system integration tests | ✓ | ✗ |

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

- **Sync Folder:** The tool only interacts with files inside a specific folder structure (`cloud-drives-sync` and `cloud-drives-sync-aux/soft-deleted`). It will never modify files outside of these directories.
- **Database:** Local metadata is stored in `metadata.db`. You can view `DATABASE_ACCESS.md` for information on how to query it manually using Python, Go, or DB Browser for SQLCipher.
- **Testing:** The `test` command runs a suite of full end-to-end integration tests mimicking complex file movements, fragmentation, soft deletions, and more. See `TEST.md` for instructions on the test suite loop.
- **Auto Build:** When built with `-tags auto`, the binary embeds `config.json.enc` and `config.salt` at compile time. This creates a self-contained binary that requires no `init` step — only the master password at runtime. Available commands are restricted to `auto`, `sync`, and `help`.