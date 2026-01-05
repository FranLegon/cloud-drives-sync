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

The database contains three main tables:

### files
Stores metadata about files across all providers.

### folders  
Stores metadata about folders across all providers.

### files_fragments
Stores information about split files (primarily for Telegram).

## Security Notes

- The database file itself is encrypted using AES-256 via SQLCipher
- The encryption key is your raw master password
- Without the correct password, the database appears as random data
- The database encryption is independent of the config file encryption (which uses `config.salt`)
