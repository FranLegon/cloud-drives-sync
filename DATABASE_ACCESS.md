# Database Access

The `metadata.db` file is encrypted using SQLCipher with your master password.

## Accessing the Database Externally

You can access and query the database outside of the CLI using tools that support SQLCipher.

### Requirements

- SQLCipher 4.x or compatible tools
- Your master password

### Using the SQLCipher command-line tool

```bash
# Open the database
sqlcipher metadata.db

# At the sqlcipher> prompt, enter your password
sqlite> PRAGMA key = 'your_master_password';

# Now you can query the database
sqlite> .tables
sqlite> SELECT * FROM files LIMIT 10;
```

### Using DB Browser for SQLCipher

1. Download [DB Browser for SQLCipher](https://sqlitebrowser.org/)
2. Open the `metadata.db` file
3. When prompted, enter your master password
4. Browse and query the database

### Using Python

```python
import sqlcipher3

conn = sqlcipher3.connect('metadata.db')
conn.execute("PRAGMA key = 'your_master_password'")

cursor = conn.cursor()
cursor.execute("SELECT * FROM files LIMIT 10")
for row in cursor:
    print(row)

conn.close()
```

### Using Go

```go
import (
    "database/sql"
    "fmt"
    "net/url"
    _ "github.com/mutecomm/go-sqlcipher/v4"
)

password := "your_master_password"
connStr := fmt.Sprintf("file:metadata.db?_pragma_key=%s", url.QueryEscape(password))

db, err := sql.Open("sqlite3", connStr)
if err != nil {
    panic(err)
}
defer db.Close()

// Query the database
rows, err := db.Query("SELECT * FROM files LIMIT 10")
// ... process rows
```

## Database Schema

The database contains four main tables:

### files
Represents the logical files (aggregated view).
- `id` (TEXT): Unique logical ID (UUID)
- `path` (TEXT): Logical path (e.g., `/folder/file.txt`)
- `name` (TEXT): File name
- `size` (INTEGER): File size in bytes
- `calculated_id` (TEXT): Content hash (SHA-256)
- `mod_time` (INTEGER): Modification timestamp (Unix)
- `status` (TEXT): 'active' or 'deleted'

### replicas
Stores information about physical file copies on each provider.
- `id` (INTEGER): Primary Key
- `file_id` (TEXT): Foreign key to `files.id`
- `calculated_id` (TEXT): Content hash
- `path` (TEXT): Path on the provider
- `name` (TEXT): Name on the provider
- `size` (INTEGER): Size in bytes
- `provider` (TEXT): Provider name (google, microsoft, telegram)
- `account_id` (TEXT): Email or Phone number
- `native_id` (TEXT): Provider-specific file ID
- `native_hash` (TEXT): Provider-specific hash
- `mod_time` (INTEGER): Modification timestamp
- `status` (TEXT): 'active' or 'deleted'
- `fragmented` (BOOLEAN): Whether the file is split into chunks
- `last_seen_at` (INTEGER): Timestamp of last successful scan

### replica_fragments
Stores details about file chunks (used for Telegram large files).
- `id` (INTEGER): Primary Key
- `replica_id` (INTEGER): Foreign key to `replicas.id`
- `fragment_number` (INTEGER): Sequence number
- `fragments_total` (INTEGER): Total number of fragments
- `size` (INTEGER): Size of this fragment
- `native_fragment_id` (TEXT): Provider ID for this fragment

### folders
Stores metadata about folders on providers.
- `id` (TEXT): Provider-specific Folder ID (Primary Key)
- `name` (TEXT): Folder name
- `path` (TEXT): Folder path
- `provider` (TEXT): Provider name
- `user_email` (TEXT): Associated email
- `user_phone` (TEXT): Associated phone
- `parent_folder_id` (TEXT): ID of parent folder
- `owner_email` (TEXT): Owner's email (for shared folders)

## Security Notes

- The database file itself is encrypted using AES-256 via SQLCipher
- The encryption key is your raw master password
- Without the correct password, the database appears as random data
- The database encryption is independent of the config file encryption (which uses `config.salt`)
