# cloud-drives-sync

A command-line tool for managing and synchronizing files across multiple cloud storage providers including Google Drive, Microsoft OneDrive for Business, and Telegram.

## Features

- **Multi-Provider Support**: Sync files across Google Drive, Microsoft OneDrive for Business, and Telegram
- **Encrypted Storage**: All configuration and metadata are encrypted using AES-256-GCM
- **Deduplication**: Detect and remove duplicate files within each provider
- **Storage Management**: Balance storage usage across accounts
- **Permission Management**: Automated sharing and permission verification
- **Safe Mode**: Dry-run mode for testing changes without making actual modifications

## Architecture

The application follows a main account + backup accounts architecture:

- **Google Drive**: One main account owns the `synched-cloud-drives` folder; backup accounts own files but not folders
- **Microsoft OneDrive**: Each backup account owns its own `synched-cloud-drives-N` folder; all are shared with the main account
- **Telegram**: Single account used as backup storage

## Installation

### Prerequisites

- Go 1.21 or later
- API credentials for:
  - Google Cloud Platform (for Google Drive)
  - Microsoft Azure (for OneDrive for Business)
  - Telegram API (optional)

### Build from Source

```bash
git clone https://github.com/FranLegon/cloud-drives-sync.git
cd cloud-drives-sync
go build -o cloud-drives-sync main.go
```

## Configuration

### First-Time Setup

1. Run the initialization command:
   ```bash
   ./cloud-drives-sync init
   ```

2. Follow the prompts to:
   - Create a master password
   - Enter API client credentials
   - Authorize your first main account

The tool will create:
- `config.json.enc`: Encrypted configuration file
- `config.salt`: Salt for key derivation
- `metadata.db`: Encrypted SQLite database

### API Credentials

#### Google Drive

1. Create a project in [Google Cloud Console](https://console.cloud.google.com)
2. Enable the Google Drive API
3. Create OAuth 2.0 credentials
4. Add `http://localhost:8080/callback` as a redirect URI
5. Note your Client ID and Client Secret

#### Microsoft OneDrive

1. Register an app in [Azure Portal](https://portal.azure.com)
2. Configure as "Multi-tenant" application
3. Add `http://localhost:8080/callback` as a redirect URI
4. Grant API permissions: `Files.ReadWrite.All`, `User.Read`, `offline_access`
5. Note your Client ID and Client Secret

#### Telegram (Optional)

1. Visit [my.telegram.org](https://my.telegram.org)
2. Create an application
3. Note your API ID and API Hash

## Commands

### `init`
Initialize the application or add main accounts.

```bash
./cloud-drives-sync init
```

### `add-account`
Add backup accounts to existing providers.

```bash
./cloud-drives-sync add-account
```

### `get-metadata`
Scan all cloud providers and update the local metadata database.

```bash
./cloud-drives-sync get-metadata
```

### `check-for-duplicates`
Find duplicate files within each provider.

```bash
./cloud-drives-sync check-for-duplicates
```

### `remove-duplicates`
Interactively remove duplicate files.

```bash
./cloud-drives-sync remove-duplicates
```

### `remove-duplicates-unsafe`
Automatically remove duplicates (keeps oldest file).

```bash
./cloud-drives-sync remove-duplicates-unsafe
```

### `share-with-main`
Verify and repair sharing permissions.

```bash
./cloud-drives-sync share-with-main
```

### `sync-providers`
Synchronize files across all cloud providers.

```bash
./cloud-drives-sync sync-providers
```

### `balance-storage`
Balance storage usage across accounts.

```bash
./cloud-drives-sync balance-storage
```

### `free-main`
Transfer all files from main account to backup accounts.

```bash
./cloud-drives-sync free-main
```

### `check-tokens`
Validate all authentication tokens.

```bash
./cloud-drives-sync check-tokens
```

## Flags

### `--safe` / `-s`
Run in dry-run mode without making actual changes.

```bash
./cloud-drives-sync remove-duplicates-unsafe --safe
```

### `--help` / `-h`
Display help information for any command.

```bash
./cloud-drives-sync init --help
```

## Security

- **Encryption**: All sensitive data is encrypted using AES-256-GCM
- **Key Derivation**: Master password is processed using Argon2id
- **Database**: SQLite database is encrypted with SQLCipher
- **Tokens**: OAuth refresh tokens are stored encrypted
- **Master Password**: Required on every execution to decrypt configuration

## Database Access

The metadata database can be accessed directly using any SQLCipher-compatible tool:

- **File**: `metadata.db`
- **Username**: `owner`
- **Password**: Your master password

## Development

### Project Structure

```
cloud-drives-sync/
├── cmd/                    # CLI commands
├── internal/
│   ├── api/               # Cloud client interface
│   ├── auth/              # OAuth and token management
│   ├── config/            # Configuration management
│   ├── crypto/            # Encryption utilities
│   ├── database/          # Database layer
│   ├── google/            # Google Drive client
│   ├── logger/            # Logging system
│   ├── microsoft/         # OneDrive client
│   ├── model/             # Data models
│   ├── task/              # Business logic
│   └── telegram/          # Telegram client
├── main.go                # Entry point
└── go.mod                 # Go module definition
```

### Dependencies

- `github.com/spf13/cobra`: CLI framework
- `github.com/manifoldco/promptui`: Interactive prompts
- `google.golang.org/api`: Google APIs
- `golang.org/x/oauth2`: OAuth 2.0
- `github.com/microsoftgraph/msgraph-sdk-go`: Microsoft Graph SDK
- `github.com/Azure/azure-sdk-for-go/sdk/azidentity`: Azure authentication
- `github.com/zelenin/go-tdlib`: Telegram client
- `github.com/mattn/go-sqlite3`: SQLite driver
- `golang.org/x/crypto`: Cryptographic functions

## Requirements

This implementation follows the specifications in `requirements/requirements_v9.txt`.

### Key Features Implemented

- ✅ Core infrastructure (models, logging, crypto, config, database)
- ✅ OAuth 2.0 authentication flow
- ✅ Google Drive client with full functionality
- ✅ Microsoft OneDrive client (basic structure)
- ✅ Telegram client (basic structure)
- ✅ CLI framework with all commands
- ✅ Metadata synchronization
- ✅ Duplicate detection and management
- ✅ Permission management
- ✅ Token validation
- ✅ Safe mode (dry-run)
- ⚠️ Cross-provider sync (placeholder)
- ⚠️ Storage balancing (placeholder)
- ⚠️ Main account clearing (placeholder)

### Notes on Implementation

This implementation provides:
1. Complete Google Drive integration with all required features
2. Architectural framework for Microsoft OneDrive and Telegram
3. All CLI commands and business logic structure
4. Full encryption and security features
5. Database layer with SQLCipher support

The Microsoft OneDrive and Telegram clients are implemented as stubs that follow the same interface pattern. To complete them, implement the methods following the same patterns as the Google Drive client.

## License

Copyright (c) 2024. All rights reserved.

## Author

FranLegon
