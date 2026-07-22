package database

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/google/uuid"
	_ "github.com/mutecomm/go-sqlcipher/v4"
)

const (
	DBFileName = "cloud-drives-sync-metadata.db"
	DBUser     = "owner"
)

// auxFolderName is the auxiliary folder name used in path matching queries.
// Overridable via SetAuxFolderName for test isolation.
var auxFolderName = "cloud-drives-sync-aux"

// SetAuxFolderName overrides the auxiliary folder name used in SQL path matching.
// Used by tests to isolate from production data.
func SetAuxFolderName(name string) {
	auxFolderName = name
}

// DB represents the database connection
type DB struct {
	conn      *sql.DB
	stmtCache map[string]*sql.Stmt
	stmtMutex sync.RWMutex
}

// GetDBPath returns the path to the database file
func GetDBPath() string {
	execPath, err := os.Executable()
	if err != nil {
		return DBFileName
	}
	return filepath.Join(filepath.Dir(execPath), DBFileName)
}

// Open opens a connection to the encrypted SQLite database
func Open(masterPassword string) (*DB, error) {
	dbPath := GetDBPath()

	// SQLCipher connection string with _pragma_key parameter
	// This is the proper way to set the encryption key for go-sqlcipher
	// _pragma_key is used instead of _key to ensure the key is set via PRAGMA before any DB access
	// Using WAL and busy_timeout to support concurrent reads/writes
	connStr := fmt.Sprintf("file:%s?_pragma_key=%s&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000", dbPath, url.QueryEscape(masterPassword))

	conn, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, err
	}

	// Verify access by querying sqlite_master
	// This will fail if the key is wrong or the database is corrupted
	var count int
	if err := conn.QueryRow("SELECT COUNT(*) FROM sqlite_master").Scan(&count); err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to access database (wrong password or corrupted database): %w", err)
	}

	// Test connection
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, err
	}

	db := &DB{conn: conn}
	return db, nil
}

// WithTx runs a function within a database transaction, managing commit/rollback.
func (db *DB) WithTx(fn func(*sql.Tx) error) (err error) {
	tx, errTx := db.conn.Begin()
	if errTx != nil {
		return fmt.Errorf("failed to begin transaction: %w", errTx)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		} else if err != nil {
			if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
				err = fmt.Errorf("transaction failed: %w (rollback error: %v)", err, rbErr)
			}
		} else {
			if commitErr := tx.Commit(); commitErr != nil {
				err = fmt.Errorf("failed to commit transaction: %w", commitErr)
				_ = tx.Rollback()
			}
		}
	}()

	return fn(tx)
}

func (db *DB) txStmt(tx *sql.Tx, query string) (*sql.Stmt, error) {
	stmt, err := db.getStmt(query)
	if err != nil {
		return nil, err
	}
	return tx.Stmt(stmt), nil
}

func (db *DB) getStmt(query string) (*sql.Stmt, error) {
	db.stmtMutex.RLock()
	stmt, ok := db.stmtCache[query]
	db.stmtMutex.RUnlock()

	if ok {
		return stmt, nil
	}

	db.stmtMutex.Lock()
	defer db.stmtMutex.Unlock()

	if db.stmtCache == nil {
		db.stmtCache = make(map[string]*sql.Stmt)
	}
	if stmt, ok := db.stmtCache[query]; ok {
		return stmt, nil
	}

	stmt, err := db.conn.Prepare(query)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare statement: %w", err)
	}

	db.stmtCache[query] = stmt
	return stmt, nil
}

func (db *DB) query(query string, args ...interface{}) (*sql.Rows, error) {
	stmt, err := db.getStmt(query)
	if err != nil {
		return nil, err
	}
	return stmt.Query(args...)
}

func (db *DB) queryRow(query string, args ...interface{}) *sql.Row {
	stmt, err := db.getStmt(query)
	if err != nil {
		return db.conn.QueryRow(query, args...)
	}
	return stmt.QueryRow(args...)
}

// Reset clears all data from the database
func (db *DB) Reset() error {
	return db.WithTx(func(tx *sql.Tx) error {
		tables := []string{
			"replica_fragments",
			"replicas",
			"files",
			"folders",
			"folder_replicas",
			"logical_folders",
			"sync_copy_log",
			"sync_runs",
		}
		for _, table := range tables {
			stmt, err := db.txStmt(tx, fmt.Sprintf("DELETE FROM %s", table))
			if err != nil {
				continue
			}
			_, _ = stmt.Exec()
			stmt.Close()
		}
		return nil
	})
}

// GetMetadataHash computes a fast hash of the current logical state of the database,
// ignoring transient fields like last_seen_at.
func (db *DB) GetMetadataHash() (string, error) {
	var version int64
	err := db.queryRow("SELECT version FROM _db_version").Scan(&version)
	if err != nil {
		return "0", nil
	}
	return strconv.FormatInt(version, 10), nil
}

// GetContentChecksum computes a stable checksum of non-system table contents.
// It ignores SQLite internal tables and transient bookkeeping columns like last_seen_at.
func (db *DB) GetContentChecksum() (string, error) {
	checksumInput, err := db.buildContentChecksumInput()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(checksumInput))
	return hex.EncodeToString(sum[:]), nil
}

func (db *DB) DumpContentChecksumInput(outputPath string) error {
	checksumInput, err := db.buildContentChecksumInput()
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, []byte(checksumInput), 0644)
}

func (db *DB) buildContentChecksumInput() (string, error) {
	tables := []string{
		"files",
		"replicas",
		"replica_fragments",
		"folders",
		"logical_folders",
		"folder_replicas",
	}

	var builder strings.Builder
	for _, table := range tables {
		columns, err := db.getComparableColumns(table)
		if err != nil {
			return "", err
		}
		if len(columns) == 0 {
			continue
		}

		query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(columns, ", "), quoteIdentifier(table))
		rows, err := db.conn.Query(query)
		if err != nil {
			return "", fmt.Errorf("query %s: %w", table, err)
		}

		serializedRows := make([]string, 0)
		for rows.Next() {
			values := make([]interface{}, len(columns))
			scanTargets := make([]interface{}, len(columns))
			for i := range values {
				scanTargets[i] = &values[i]
			}
			if err := rows.Scan(scanTargets...); err != nil {
				rows.Close()
				return "", fmt.Errorf("scan %s: %w", table, err)
			}
			serializedRows = append(serializedRows, serializeNamedRow(table, columns, values))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return "", fmt.Errorf("iterate %s: %w", table, err)
		}
		rows.Close()

		sort.Strings(serializedRows)
		fmt.Fprintf(&builder, "table:%s\n", table)
		fmt.Fprintf(&builder, "columns:%s\n", strings.Join(columns, ","))
		for _, row := range serializedRows {
			fmt.Fprintf(&builder, "row:%s\n", row)
		}
		fmt.Fprintln(&builder, "--")
	}

	return builder.String(), nil
}

func (db *DB) getComparableColumns(table string) ([]string, error) {
	rows, err := db.conn.Query(fmt.Sprintf("PRAGMA table_info(%s)", quoteIdentifier(table)))
	if err != nil {
		return nil, fmt.Errorf("table info %s: %w", table, err)
	}
	defer rows.Close()

	columns := make([]string, 0)
	for rows.Next() {
		var cid int
		var name string
		var colType string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scan table info %s: %w", table, err)
		}
		if shouldExcludeChecksumColumn(table, name, colType, pk) {
			continue
		}
		columns = append(columns, quoteIdentifier(name))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table info %s: %w", table, err)
	}
	return columns, nil
}

func shouldExcludeChecksumColumn(table, column, columnType string, pk int) bool {
	if column == "last_seen_at" {
		return true
	}
	if table == "folders" && column == "user_email" {
		return true
	}
	if column == "id" && pk > 0 && isIntegerColumnType(columnType) {
		return true
	}
	return false
}

func isIntegerColumnType(columnType string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(columnType))
	return strings.Contains(normalized, "INT")
}

func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func serializeNamedRow(table string, columns []string, values []interface{}) string {
	normalizedValues := normalizeChecksumRow(table, columns, values)
	parts := make([]string, len(normalizedValues))
	for i, value := range normalizedValues {
		parts[i] = fmt.Sprintf("%s:%s", strings.Trim(columns[i], `"`), normalizeChecksumValue(value))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

func normalizeChecksumRow(table string, columns []string, values []interface{}) []interface{} {
	normalized := append([]interface{}(nil), values...)
	if table != "replicas" {
		return normalized
	}

	columnIndex := make(map[string]int, len(columns))
	for i, column := range columns {
		columnIndex[strings.Trim(column, `"`)] = i
	}

	nativeHashIndex, ok := columnIndex["native_hash"]
	if !ok {
		return normalized
	}
	if fmt.Sprintf("%v", normalized[nativeHashIndex]) != model.NativeHashShortcut {
		return normalized
	}

	if sizeIndex, ok := columnIndex["size"]; ok {
		normalized[sizeIndex] = int64(0)
	}

	return normalized
}

func normalizeChecksumValue(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case []byte:
		return fmt.Sprintf(`{"__bytes__":"%s"}`, hex.EncodeToString(v))
	case bool:
		if v {
			return "1"
		}
		return "0"
	default:
		return fmt.Sprintf("%v", v)
	}
}

// Close closes the database connection
func (db *DB) Close() error {
	if db.conn != nil {
		db.stmtMutex.Lock()
		for _, stmt := range db.stmtCache {
			stmt.Close()
		}
		db.stmtCache = nil
		db.stmtMutex.Unlock()
		return db.conn.Close()
	}
	return nil
}

// Initialize creates the database schema
func (db *DB) Initialize() error {
	return db.WithTx(func(tx *sql.Tx) error {
		schema := `
		CREATE TABLE IF NOT EXISTS files (
			id TEXT PRIMARY KEY,
			path TEXT NOT NULL,
			name TEXT NOT NULL,
			size INTEGER NOT NULL,
			google_drive_md5 TEXT NOT NULL DEFAULT '',
			mod_time INTEGER NOT NULL,
			status TEXT NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
		CREATE INDEX IF NOT EXISTS idx_files_google_drive_md5 ON files(google_drive_md5);
		CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);

		CREATE TABLE IF NOT EXISTS replicas (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_id TEXT,
			path TEXT NOT NULL,
			name TEXT NOT NULL,
			size INTEGER NOT NULL,
			provider TEXT NOT NULL,
			account_id TEXT NOT NULL,
			native_id TEXT NOT NULL,
			native_hash TEXT,
			mod_time INTEGER NOT NULL,
			status TEXT NOT NULL,
			fragmented BOOLEAN NOT NULL DEFAULT 0,
			last_seen_at INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_replicas_file_id ON replicas(file_id);
		CREATE INDEX IF NOT EXISTS idx_replicas_provider ON replicas(provider);
		CREATE INDEX IF NOT EXISTS idx_replicas_account_id ON replicas(account_id);
		CREATE INDEX IF NOT EXISTS idx_replicas_native_id_old ON replicas(native_id);
		CREATE INDEX IF NOT EXISTS idx_replicas_status ON replicas(status);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_replicas_unique ON replicas(provider, account_id, native_id);

		CREATE INDEX IF NOT EXISTS idx_replicas_native_id ON replicas(provider, native_id);
		CREATE INDEX IF NOT EXISTS idx_replicas_provider_status ON replicas(provider, status);
		CREATE INDEX IF NOT EXISTS idx_replicas_last_seen ON replicas(last_seen_at);

		CREATE TABLE IF NOT EXISTS replica_fragments (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			replica_id INTEGER NOT NULL,
			fragment_number INTEGER NOT NULL,
			fragments_total INTEGER NOT NULL,
			size INTEGER NOT NULL,
			native_fragment_id TEXT NOT NULL,
			FOREIGN KEY(replica_id) REFERENCES replicas(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_replica_fragments_replica_id ON replica_fragments(replica_id);
		CREATE INDEX IF NOT EXISTS idx_replica_fragments_native_id ON replica_fragments(native_fragment_id);

		CREATE TABLE IF NOT EXISTS folders (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			path TEXT NOT NULL,
			provider TEXT NOT NULL,
			user_email TEXT,
			user_phone TEXT,
			parent_folder_id TEXT,
			owner_email TEXT
		);

		CREATE INDEX IF NOT EXISTS idx_folders_provider ON folders(provider);
		CREATE INDEX IF NOT EXISTS idx_folders_path ON folders(path);

		-- New-model folder tables (SPEC): a provider-agnostic logical_folder with one
		-- folder_replica per provider/account. Populated by the Phase 2 folder sync logic.
		CREATE TABLE IF NOT EXISTS logical_folders (
			id TEXT PRIMARY KEY,
			path TEXT NOT NULL,
			name TEXT NOT NULL,
			parent_logical_folder_id TEXT,
			status TEXT NOT NULL DEFAULT 'active'
		);

		CREATE INDEX IF NOT EXISTS idx_logical_folders_path ON logical_folders(path);
		CREATE INDEX IF NOT EXISTS idx_logical_folders_parent ON logical_folders(parent_logical_folder_id);
		CREATE INDEX IF NOT EXISTS idx_logical_folders_status ON logical_folders(status);

		CREATE TABLE IF NOT EXISTS folder_replicas (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			logical_folder_id TEXT NOT NULL,
			provider TEXT NOT NULL,
			account_id TEXT NOT NULL,
			native_folder_id TEXT NOT NULL,
			owner TEXT DEFAULT '',
			last_seen_at INTEGER NOT NULL DEFAULT 0,
			FOREIGN KEY(logical_folder_id) REFERENCES logical_folders(id) ON DELETE CASCADE
		);

		CREATE INDEX IF NOT EXISTS idx_folder_replicas_logical_folder_id ON folder_replicas(logical_folder_id);
		CREATE INDEX IF NOT EXISTS idx_folder_replicas_provider ON folder_replicas(provider);
		CREATE INDEX IF NOT EXISTS idx_folder_replicas_account_id ON folder_replicas(account_id);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_folder_replicas_unique ON folder_replicas(provider, account_id, native_folder_id);

		CREATE TABLE IF NOT EXISTS sync_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			started_at INTEGER NOT NULL,
			completed_at INTEGER,
			last_completed_step INTEGER NOT NULL DEFAULT 0,
			safe_mode BOOLEAN NOT NULL DEFAULT 0
		);

		CREATE TABLE IF NOT EXISTS sync_copy_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sync_run_id INTEGER NOT NULL,
			file_id TEXT NOT NULL,
			target_provider TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			FOREIGN KEY(sync_run_id) REFERENCES sync_runs(id) ON DELETE CASCADE
		);

		CREATE UNIQUE INDEX IF NOT EXISTS idx_sync_copy_log_unique ON sync_copy_log(sync_run_id, file_id, target_provider);

		CREATE TABLE IF NOT EXISTS _db_version (version INTEGER);
		INSERT INTO _db_version (version) SELECT 0 WHERE NOT EXISTS (SELECT 1 FROM _db_version);

		CREATE TRIGGER IF NOT EXISTS files_ai AFTER INSERT ON files BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS files_au AFTER UPDATE ON files
		WHEN OLD.path IS NOT NEW.path OR OLD.name IS NOT NEW.name OR OLD.size IS NOT NEW.size OR OLD.google_drive_md5 IS NOT NEW.google_drive_md5 OR OLD.mod_time IS NOT NEW.mod_time OR OLD.status IS NOT NEW.status
		BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS files_ad AFTER DELETE ON files BEGIN UPDATE _db_version SET version = version + 1; END;

		CREATE TRIGGER IF NOT EXISTS replicas_ai AFTER INSERT ON replicas BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS replicas_au AFTER UPDATE ON replicas 
		WHEN OLD.file_id IS NOT NEW.file_id OR OLD.path IS NOT NEW.path OR OLD.name IS NOT NEW.name OR OLD.size IS NOT NEW.size OR OLD.provider IS NOT NEW.provider OR OLD.account_id IS NOT NEW.account_id OR OLD.native_id IS NOT NEW.native_id OR OLD.native_hash IS NOT NEW.native_hash OR OLD.mod_time IS NOT NEW.mod_time OR OLD.status IS NOT NEW.status OR OLD.fragmented IS NOT NEW.fragmented OR OLD.owner IS NOT NEW.owner
		BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS replicas_ad AFTER DELETE ON replicas BEGIN UPDATE _db_version SET version = version + 1; END;

		CREATE TRIGGER IF NOT EXISTS folders_ai AFTER INSERT ON folders BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS folders_au AFTER UPDATE ON folders
		WHEN OLD.name IS NOT NEW.name OR OLD.path IS NOT NEW.path OR OLD.provider IS NOT NEW.provider OR OLD.parent_folder_id IS NOT NEW.parent_folder_id OR OLD.owner_email IS NOT NEW.owner_email
		BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS folders_ad AFTER DELETE ON folders BEGIN UPDATE _db_version SET version = version + 1; END;

		CREATE TRIGGER IF NOT EXISTS logical_folders_ai AFTER INSERT ON logical_folders BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS logical_folders_au AFTER UPDATE ON logical_folders
		WHEN OLD.path IS NOT NEW.path OR OLD.name IS NOT NEW.name OR OLD.parent_logical_folder_id IS NOT NEW.parent_logical_folder_id OR OLD.status IS NOT NEW.status
		BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS logical_folders_ad AFTER DELETE ON logical_folders BEGIN UPDATE _db_version SET version = version + 1; END;

		CREATE TRIGGER IF NOT EXISTS folder_replicas_ai AFTER INSERT ON folder_replicas BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS folder_replicas_au AFTER UPDATE ON folder_replicas
		WHEN OLD.logical_folder_id IS NOT NEW.logical_folder_id OR OLD.provider IS NOT NEW.provider OR OLD.account_id IS NOT NEW.account_id OR OLD.native_folder_id IS NOT NEW.native_folder_id OR OLD.owner IS NOT NEW.owner
		BEGIN UPDATE _db_version SET version = version + 1; END;
		CREATE TRIGGER IF NOT EXISTS folder_replicas_ad AFTER DELETE ON folder_replicas BEGIN UPDATE _db_version SET version = version + 1; END;
		`

		if _, err := tx.Exec(schema); err != nil {
			return fmt.Errorf("failed to create schema: %w", err)
		}

		// Migrations
		_, _ = tx.Exec("ALTER TABLE replicas ADD COLUMN last_seen_at INTEGER DEFAULT 0")
		_, _ = tx.Exec("ALTER TABLE replicas ADD COLUMN owner TEXT DEFAULT ''")

		// Rebuild folders_au trigger to add WHEN clause (idempotent: drop then create)
		_, _ = tx.Exec("DROP TRIGGER IF EXISTS folders_au")
		_, _ = tx.Exec(`CREATE TRIGGER IF NOT EXISTS folders_au AFTER UPDATE ON folders
		WHEN OLD.name IS NOT NEW.name OR OLD.path IS NOT NEW.path OR OLD.provider IS NOT NEW.provider OR OLD.parent_folder_id IS NOT NEW.parent_folder_id OR OLD.owner_email IS NOT NEW.owner_email
		BEGIN UPDATE _db_version SET version = version + 1; END`)

		// Rebuild files_au trigger to add WHEN clause (idempotent: drop then create)
		_, _ = tx.Exec("DROP TRIGGER IF EXISTS files_au")
		_, _ = tx.Exec(`CREATE TRIGGER IF NOT EXISTS files_au AFTER UPDATE ON files
		WHEN OLD.path IS NOT NEW.path OR OLD.name IS NOT NEW.name OR OLD.size IS NOT NEW.size OR OLD.google_drive_md5 IS NOT NEW.google_drive_md5 OR OLD.mod_time IS NOT NEW.mod_time OR OLD.status IS NOT NEW.status
		BEGIN UPDATE _db_version SET version = version + 1; END`)

		return nil
	})
}

// InsertFile inserts a file record into the database
func normalizeReplicaOwner(replica *model.Replica) string {
	if replica == nil {
		return ""
	}
	if strings.TrimSpace(replica.Owner) != "" {
		return replica.Owner
	}
	return replica.AccountID
}

func (db *DB) InsertFile(file *model.File) error {
	return db.WithTx(func(tx *sql.Tx) error {
		fileQuery := `
		INSERT OR REPLACE INTO files (
			id, path, name, size, google_drive_md5, mod_time, status
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		`
		fileStmt, err := db.txStmt(tx, fileQuery)
		if err != nil {
			return fmt.Errorf("failed to prepare file statement: %w", err)
		}
		defer fileStmt.Close()

		if _, err := fileStmt.Exec(
			file.ID, file.Path, file.Name, file.Size, file.GoogleDriveMD5, file.ModTime.Unix(), file.Status); err != nil {
			return fmt.Errorf("failed to insert file: %w", err)
		}

		if len(file.Replicas) > 0 {
			replicaQuery := `
			INSERT OR REPLACE INTO replicas (
				file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`
			replicaStmt, err := db.txStmt(tx, replicaQuery)
			if err != nil {
				return fmt.Errorf("failed to prepare replica statement: %w", err)
			}
			defer replicaStmt.Close()

			for _, replica := range file.Replicas {
				replica.Owner = normalizeReplicaOwner(replica)
				if _, err := replicaStmt.Exec(
					file.ID, replica.Path, replica.Name, replica.Size,
					string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
					replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner); err != nil {
					return fmt.Errorf("failed to insert replica: %w", err)
				}
			}
		}
		return nil
	})
}

// BatchInsertFiles inserts multiple files (replicas and fragments) in a single transaction
func (db *DB) BatchInsertFiles(files []*model.File) error {
	if len(files) == 0 {
		return nil
	}

	return db.WithTx(func(tx *sql.Tx) error {
		// Upsert replicas using ON CONFLICT logic to preserve file_id if it exists.
		// We rely on the unique index (provider, account_id, native_id).
		// usage of RETURNING id requires SQLite 3.35+
		// IMPORTANT: Don't resurrect 'deleted' replicas with stale native_id after file transfers
		now := time.Now().Unix()
		replicaQuery := `
			INSERT INTO replicas (
				path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, file_id, last_seen_at, owner
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
			ON CONFLICT(provider, account_id, native_id) DO UPDATE SET
				path=excluded.path,
				name=excluded.name,
				size=excluded.size,
				native_hash=excluded.native_hash,
				mod_time=excluded.mod_time,
				status=CASE 
					WHEN replicas.status = 'deleted' AND (replicas.native_hash = excluded.native_hash OR excluded.native_hash = '' OR replicas.native_hash = '') THEN 'deleted'
					ELSE excluded.status 
				END,
				fragmented=excluded.fragmented,
				last_seen_at=excluded.last_seen_at,
				owner=excluded.owner
		`
		replicaStmt, err := db.txStmt(tx, replicaQuery)
		if err != nil {
			return err
		}
		defer replicaStmt.Close()

		// Prepare ID lookup statement
		idStmt, err := db.txStmt(tx, `SELECT id FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ?`)
		if err != nil {
			return err
		}
		defer idStmt.Close()

		// Prepare fragment statements
		deleteFragmentsStmt, err := db.txStmt(tx, `DELETE FROM replica_fragments WHERE replica_id = ?`)
		if err != nil {
			return err
		}
		defer deleteFragmentsStmt.Close()

		fragmentStmt, err := db.txStmt(tx, `
			INSERT INTO replica_fragments (
				replica_id, fragment_number, fragments_total, size, native_fragment_id
			) VALUES (?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		defer fragmentStmt.Close()

		for _, file := range files {
			for _, replica := range file.Replicas {
				replica.Owner = normalizeReplicaOwner(replica)
				_, err := replicaStmt.Exec(
					replica.Path, replica.Name, replica.Size,
					string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
					replica.ModTime.Unix(), replica.Status, replica.Fragmented, now, replica.Owner)

				if err != nil {
					return fmt.Errorf("failed to upsert replica: %w", err)
				}

				if len(replica.Fragments) > 0 {
					var replicaID int64
					err = idStmt.QueryRow(string(replica.Provider), replica.AccountID, replica.NativeID).Scan(&replicaID)
					if err != nil {
						return fmt.Errorf("failed to get replica ID: %w", err)
					}

					// Clear old fragments
					if _, err := deleteFragmentsStmt.Exec(replicaID); err != nil {
						return fmt.Errorf("failed to clear fragments: %w", err)
					}

					// Insert new fragments
					for _, frag := range replica.Fragments {
						_, err = fragmentStmt.Exec(
							replicaID, frag.FragmentNumber, frag.FragmentsTotal, frag.Size, frag.NativeFragmentID)
						if err != nil {
							return fmt.Errorf("failed to insert fragment: %w", err)
						}
					}
				}
			}
		}

		return nil
	})
}

// UpdateReplicaOwner updates the owner (account_id) of a replica.
// This is used during FreeMain when ownership is transferred.
func (db *DB) UpdateReplicaOwner(provider string, oldAccountID, nativeID, newAccountID string) error {
	return db.WithTx(func(tx *sql.Tx) error {
		// Check if target replica already exists to avoid UNIQUE constraint violation
		var exists int
		checkQuery := `SELECT 1 FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ?`
		checkStmt, err := db.txStmt(tx, checkQuery)
		if err != nil {
			return fmt.Errorf("failed to prepare check statement: %w", err)
		}
		defer checkStmt.Close()

		err = checkStmt.QueryRow(provider, newAccountID, nativeID).Scan(&exists)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("failed to check existing replica: %w", err)
		}

		if exists == 1 {
			// Target already exists, so we just remove the old one to reflect the move/change
			// (The new owner is already tracked, so we don't need to update the old record to it)
			delQuery := `DELETE FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ?`
			delStmt, err := db.txStmt(tx, delQuery)
			if err != nil {
				return fmt.Errorf("failed to prepare delete statement: %w", err)
			}
			defer delStmt.Close()
			if _, err := delStmt.Exec(provider, oldAccountID, nativeID); err != nil {
				return fmt.Errorf("failed to delete old replica: %w", err)
			}
			return nil
		}

		query := `
			UPDATE replicas
			SET account_id = ?, owner = ?
			WHERE provider = ? AND account_id = ? AND native_id = ?
			  AND (account_id IS NOT ? OR owner IS NOT ?)
		`
		updateStmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare update statement: %w", err)
		}
		defer updateStmt.Close()

		res, err := updateStmt.Exec(newAccountID, newAccountID, provider, oldAccountID, nativeID, newAccountID, newAccountID)
		if err != nil {
			return fmt.Errorf("failed to update replica owner: %w", err)
		}

		rows, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("failed to get rows affected: %w", err)
		}
		if rows == 0 {
			var alreadyUpdated int
			alreadyUpdatedQuery := `SELECT 1 FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ? AND owner = ?`
			alreadyUpdatedStmt, stmtErr := db.txStmt(tx, alreadyUpdatedQuery)
			if stmtErr != nil {
				return fmt.Errorf("failed to prepare already-updated check statement: %w", stmtErr)
			}
			defer alreadyUpdatedStmt.Close()

			checkErr := alreadyUpdatedStmt.QueryRow(provider, newAccountID, nativeID, newAccountID).Scan(&alreadyUpdated)
			if checkErr == nil && alreadyUpdated == 1 {
				return nil
			}
			if checkErr != nil && checkErr != sql.ErrNoRows {
				return fmt.Errorf("failed to verify already-updated replica: %w", checkErr)
			}
			return fmt.Errorf("no replica found to update (prov=%s, acc=%s, id=%s)", provider, oldAccountID, nativeID)
		}
		return nil
	})
}

// InsertReplica inserts a replica record and its fragments into the database within a transaction
func (db *DB) InsertReplica(replica *model.Replica) error {
	replica.Owner = normalizeReplicaOwner(replica)
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		INSERT INTO replicas (
			file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()

		res, err := stmt.Exec(
			replica.FileID, replica.Path, replica.Name, replica.Size,
			string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
			replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner)
		if err != nil {
			return fmt.Errorf("failed to insert replica: %w", err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("failed to get replica ID: %w", err)
		}
		replica.ID = id

		if replica.Fragmented && len(replica.Fragments) > 0 {
			fragQuery := `
			INSERT INTO replica_fragments (
				replica_id, fragment_number, fragments_total, size, native_fragment_id
			) VALUES (?, ?, ?, ?, ?)
			`
			fragStmt, err := db.txStmt(tx, fragQuery)
			if err != nil {
				return fmt.Errorf("failed to prepare fragment statement: %w", err)
			}
			defer fragStmt.Close()

			for _, frag := range replica.Fragments {
				resFrag, err := fragStmt.Exec(
					replica.ID, frag.FragmentNumber, frag.FragmentsTotal, frag.Size, frag.NativeFragmentID)
				if err != nil {
					return fmt.Errorf("failed to insert fragment: %w", err)
				}
				fragID, err := resFrag.LastInsertId()
				if err == nil {
					frag.ID = fragID
					frag.ReplicaID = replica.ID
				}
			}
		}
		return nil
	})
}

// UpsertReplicaByNativeID inserts or updates a replica keyed by provider/account/native_id.
func (db *DB) UpsertReplicaByNativeID(replica *model.Replica, lastSeenAt int64) error {
	replica.Owner = normalizeReplicaOwner(replica)
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		INSERT INTO replicas (
			file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner, last_seen_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider, account_id, native_id) DO UPDATE SET
			file_id=CASE WHEN excluded.file_id != '' THEN excluded.file_id ELSE replicas.file_id END,
			path=excluded.path,
			name=excluded.name,
			size=excluded.size,
			native_hash=excluded.native_hash,
			mod_time=excluded.mod_time,
			status=excluded.status,
			fragmented=excluded.fragmented,
			owner=excluded.owner,
			last_seen_at=excluded.last_seen_at
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare upsert statement: %w", err)
		}
		defer stmt.Close()

		if _, err := stmt.Exec(
			replica.FileID, replica.Path, replica.Name, replica.Size,
			string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
			replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner, lastSeenAt,
		); err != nil {
			return fmt.Errorf("failed to upsert replica by native id: %w", err)
		}

		idStmt, err := db.txStmt(tx, `SELECT id FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ?`)
		if err != nil {
			return fmt.Errorf("failed to prepare replica id lookup: %w", err)
		}
		defer idStmt.Close()
		if err := idStmt.QueryRow(string(replica.Provider), replica.AccountID, replica.NativeID).Scan(&replica.ID); err != nil {
			return fmt.Errorf("failed to load upserted replica id: %w", err)
		}
		return nil
	})
}

// UpsertReplica inserts or updates a replica record
func (db *DB) UpsertReplica(replica *model.Replica) error {
	replica.Owner = normalizeReplicaOwner(replica)
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		INSERT OR REPLACE INTO replicas (
			id, file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare upsert replica statement: %w", err)
		}
		defer stmt.Close()

		_, err = stmt.Exec(
			replica.ID, replica.FileID, replica.Path, replica.Name, replica.Size,
			string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
			replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner)
		if err != nil {
			return fmt.Errorf("failed to upsert replica: %w", err)
		}
		return nil
	})
}

// InsertReplicaFragment inserts a fragment record into the database
func (db *DB) InsertReplicaFragment(fragment *model.ReplicaFragment) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		INSERT INTO replica_fragments (
			replica_id, fragment_number, fragments_total, size, native_fragment_id
		) VALUES (?, ?, ?, ?, ?)
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare fragment statement: %w", err)
		}
		defer stmt.Close()

		res, err := stmt.Exec(
			fragment.ReplicaID, fragment.FragmentNumber, fragment.FragmentsTotal, fragment.Size, fragment.NativeFragmentID)
		if err != nil {
			return fmt.Errorf("failed to insert fragment: %w", err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("failed to get fragment ID: %w", err)
		}
		fragment.ID = id
		return nil
	})
}

// InsertFolder inserts a folder record into the database
func (db *DB) InsertFolder(folder *model.Folder) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		INSERT INTO folders (
			id, name, path, provider, user_email, user_phone, parent_folder_id, owner_email
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			path=excluded.path,
			provider=excluded.provider,
			user_email=excluded.user_email,
			user_phone=excluded.user_phone,
			parent_folder_id=excluded.parent_folder_id,
			owner_email=excluded.owner_email
		WHERE folders.name != excluded.name
		   OR folders.path != excluded.path
		   OR folders.provider != excluded.provider
		   OR COALESCE(folders.user_email, '') != COALESCE(excluded.user_email, '')
		   OR COALESCE(folders.user_phone, '') != COALESCE(excluded.user_phone, '')
		   OR COALESCE(folders.parent_folder_id, '') != COALESCE(excluded.parent_folder_id, '')
		   OR COALESCE(folders.owner_email, '') != COALESCE(excluded.owner_email, '')
		`

		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare folder statement: %w", err)
		}
		defer stmt.Close()

		_, err = stmt.Exec(
			folder.ID, folder.Name, folder.Path, string(folder.Provider),
			folder.UserEmail, folder.UserPhone, folder.ParentFolderID, folder.OwnerEmail,
		)
		if err != nil {
			return fmt.Errorf("failed to insert folder: %w", err)
		}
		return nil
	})
}

// BatchInsertFolders inserts multiple folders in a single transaction
func (db *DB) BatchInsertFolders(folders []*model.Folder) error {
	return db.WithTx(func(tx *sql.Tx) error {
		stmt, err := db.txStmt(tx, `
		INSERT INTO folders (
			id, name, path, provider, user_email, user_phone, parent_folder_id, owner_email
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name,
			path=excluded.path,
			provider=excluded.provider,
			user_email=excluded.user_email,
			user_phone=excluded.user_phone,
			parent_folder_id=excluded.parent_folder_id,
			owner_email=excluded.owner_email
		WHERE folders.name != excluded.name
		   OR folders.path != excluded.path
		   OR folders.provider != excluded.provider
		   OR COALESCE(folders.user_email, '') != COALESCE(excluded.user_email, '')
		   OR COALESCE(folders.user_phone, '') != COALESCE(excluded.user_phone, '')
		   OR COALESCE(folders.parent_folder_id, '') != COALESCE(excluded.parent_folder_id, '')
		   OR COALESCE(folders.owner_email, '') != COALESCE(excluded.owner_email, '')
		`)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, folder := range folders {
			_, err := stmt.Exec(
				folder.ID, folder.Name, folder.Path, string(folder.Provider),
				folder.UserEmail, folder.UserPhone, folder.ParentFolderID, folder.OwnerEmail,
			)
			if err != nil {
				return err
			}
		}

		return nil
	})
}

// GetAllFolders returns all folders from DB
func (db *DB) GetAllFolders() ([]*model.Folder, error) {
	rows, err := db.query("SELECT id, name, path, provider, user_email, user_phone, parent_folder_id, owner_email FROM folders")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []*model.Folder
	for rows.Next() {
		var f model.Folder
		var provider string
		if err := rows.Scan(&f.ID, &f.Name, &f.Path, &provider, &f.UserEmail, &f.UserPhone, &f.ParentFolderID, &f.OwnerEmail); err != nil {
			return nil, err
		}
		f.Provider = model.Provider(provider)
		folders = append(folders, &f)
	}
	return folders, nil
}

// GetFolderByPathAndAccount retrieves a folder by its path, provider, and account identifier
func (db *DB) GetFolderByPathAndAccount(path string, provider model.Provider, accountID string) (*model.Folder, error) {
	query := `
	SELECT id, name, path, provider, user_email, user_phone, parent_folder_id, owner_email
	FROM folders
	WHERE path = ? AND provider = ? AND (user_email = ? OR user_phone = ?)
	`
	row := db.queryRow(query, path, string(provider), accountID, accountID)

	var f model.Folder
	var prov string
	err := row.Scan(&f.ID, &f.Name, &f.Path, &prov, &f.UserEmail, &f.UserPhone, &f.ParentFolderID, &f.OwnerEmail)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // not found
		}
		return nil, err
	}
	f.Provider = model.Provider(prov)
	return &f, nil
}

// --- New-model folder tables (SPEC): logical_folders + folder_replicas ---
// These are populated by the Phase 2 folder sync logic.

// InsertLogicalFolder inserts or updates a logical_folder record idempotently.
func (db *DB) InsertLogicalFolder(f *model.LogicalFolder) error {
	return db.WithTx(func(tx *sql.Tx) error {
		var parent interface{}
		if f.ParentLogicalFolderID != "" {
			parent = f.ParentLogicalFolderID
		}
		status := f.Status
		if status == "" {
			status = "active"
		}
		_, err := tx.Exec(
			`INSERT INTO logical_folders (id, path, name, parent_logical_folder_id, status)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO UPDATE SET
				path=excluded.path,
				name=excluded.name,
				parent_logical_folder_id=excluded.parent_logical_folder_id,
				status=excluded.status`,
			f.ID, f.Path, f.Name, parent, status)
		if err != nil {
			return fmt.Errorf("failed to insert logical_folder: %w", err)
		}
		return nil
	})
}

// GetAllLogicalFolders returns all logical_folder records.
func (db *DB) GetAllLogicalFolders() ([]*model.LogicalFolder, error) {
	rows, err := db.query(`SELECT id, path, name, parent_logical_folder_id, status FROM logical_folders`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []*model.LogicalFolder
	for rows.Next() {
		f := &model.LogicalFolder{}
		var parent sql.NullString
		if err := rows.Scan(&f.ID, &f.Path, &f.Name, &parent, &f.Status); err != nil {
			return nil, err
		}
		if parent.Valid {
			f.ParentLogicalFolderID = parent.String
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

// InsertFolderReplica inserts or updates a folder_replica record idempotently.
func (db *DB) InsertFolderReplica(r *model.FolderReplica) error {
	return db.WithTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`INSERT INTO folder_replicas
			 (logical_folder_id, provider, account_id, native_folder_id, owner, last_seen_at)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(provider, account_id, native_folder_id) DO UPDATE SET
				logical_folder_id=excluded.logical_folder_id,
				owner=excluded.owner,
				last_seen_at=excluded.last_seen_at`,
			r.LogicalFolderID, string(r.Provider), r.AccountID, r.NativeFolderID, r.Owner, r.LastSeenAt)
		if err != nil {
			return fmt.Errorf("failed to insert folder_replica: %w", err)
		}
		return nil
	})
}

// GetFolderReplicas returns all folder_replica records for a logical_folder.
func (db *DB) GetFolderReplicas(logicalFolderID string) ([]*model.FolderReplica, error) {
	rows, err := db.query(
		`SELECT id, logical_folder_id, provider, account_id, native_folder_id, owner, last_seen_at
		 FROM folder_replicas WHERE logical_folder_id = ?`, logicalFolderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var replicas []*model.FolderReplica
	for rows.Next() {
		r := &model.FolderReplica{}
		var prov string
		var owner sql.NullString
		if err := rows.Scan(&r.ID, &r.LogicalFolderID, &prov, &r.AccountID, &r.NativeFolderID, &owner, &r.LastSeenAt); err != nil {
			return nil, err
		}
		r.Provider = model.Provider(prov)
		if owner.Valid {
			r.Owner = owner.String
		}
		replicas = append(replicas, r)
	}
	return replicas, rows.Err()
}

// GetAllFolderReplicaViews returns cache-ready folder rows resolved from logical_folders joined with
// folder_replicas. This is the runtime read path for folder convergence.
func (db *DB) GetAllFolderReplicaViews() ([]*model.Folder, error) {
	rows, err := db.query(`
		SELECT fr.native_folder_id, lf.name, lf.path, fr.provider, fr.account_id, lf.parent_logical_folder_id, fr.owner
		FROM logical_folders lf
		JOIN folder_replicas fr ON fr.logical_folder_id = lf.id
		WHERE lf.status != 'deleted'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []*model.Folder
	for rows.Next() {
		f := &model.Folder{}
		var provider string
		var parent sql.NullString
		var owner sql.NullString
		if err := rows.Scan(&f.ID, &f.Name, &f.Path, &provider, &f.UserEmail, &parent, &owner); err != nil {
			return nil, err
		}
		f.Provider = model.Provider(provider)
		if f.Provider == model.ProviderTelegram {
			f.UserPhone = f.UserEmail
			f.UserEmail = ""
		}
		if parent.Valid {
			f.ParentFolderID = parent.String
		}
		if owner.Valid {
			f.OwnerEmail = owner.String
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

// UpdateLogicalFilesGoogleMD5 populates each logical file's canonical Google Drive MD5 identity from
// its most recent active Google replica's fingerprint. Only rows whose value actually changes are
// updated, so repeated runs are idempotent (do not bump the metadata version).
func (db *DB) UpdateLogicalFilesGoogleMD5() error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		UPDATE files
		SET google_drive_md5 = g.md5
		FROM (
			SELECT file_id, native_hash AS md5,
				ROW_NUMBER() OVER (PARTITION BY file_id ORDER BY mod_time DESC) AS rn
			FROM replicas
			WHERE LOWER(provider) = 'google'
			AND status = 'active'
			AND native_hash IS NOT NULL
			AND native_hash != ''
		) g
		WHERE files.id = g.file_id
		AND g.rn = 1
		AND files.google_drive_md5 IS NOT g.md5
		`
		if _, err := tx.Exec(query); err != nil {
			return fmt.Errorf("failed to update google_drive_md5: %w", err)
		}
		return nil
	})
}

// GetAllFiles returns all files with replicas loaded in a single batch query
func (db *DB) GetAllFiles() ([]*model.File, error) {
	query := `
	SELECT id, path, name, size, google_drive_md5, mod_time, status
	FROM files
	`

	rows, err := db.query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	fileMap := make(map[string]*model.File)
	for rows.Next() {
		file := &model.File{}
		var modTime int64
		err := rows.Scan(
			&file.ID, &file.Path, &file.Name, &file.Size, &file.GoogleDriveMD5, &modTime, &file.Status,
		)
		if err != nil {
			return nil, err
		}
		file.ModTime = time.Unix(modTime, 0)
		files = append(files, file)
		fileMap[file.ID] = file
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return files, nil
	}

	// Batch-load all replicas in a single query instead of N queries
	replicas, err := db.getAllReplicas()
	if err != nil {
		return nil, err
	}

	// Assign replicas to their parent files
	for _, r := range replicas {
		if file, ok := fileMap[r.FileID]; ok {
			file.Replicas = append(file.Replicas, r)
		}
	}

	// Load fragments for fragmented replicas
	fragmented := make([]*model.Replica, 0, len(replicas))
	for _, r := range replicas {
		if r.Fragmented {
			fragmented = append(fragmented, r)
		}
	}
	if len(fragmented) > 0 {
		if err := db.batchLoadFragments(fragmented); err != nil {
			return nil, err
		}
	}

	return files, nil
}

// getAllReplicas loads all replicas from the database in one query
func (db *DB) getAllReplicas() ([]*model.Replica, error) {
	query := `
	SELECT id, file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE file_id IS NOT NULL
	`
	rows, err := db.query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var replicas []*model.Replica
	for rows.Next() {
		r := &model.Replica{}
		var providerStr string
		var modTime int64
		var owner sql.NullString
		err := rows.Scan(&r.ID, &r.FileID, &r.Path, &r.Name, &r.Size,
			&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented, &owner)
		if err != nil {
			return nil, err
		}
		if owner.Valid {
			r.Owner = owner.String
		}
		r.Provider = model.Provider(providerStr)
		r.ModTime = time.Unix(modTime, 0)
		replicas = append(replicas, r)
	}
	return replicas, rows.Err()
}

// batchLoadFragments loads fragments for the specified fragmented replicas efficiently
func (db *DB) batchLoadFragments(replicas []*model.Replica) error {
	if len(replicas) == 0 {
		return nil
	}

	// Extract unique replica IDs
	replicaMap := make(map[int64]*model.Replica, len(replicas))
	replicaIDs := make([]int64, 0, len(replicas))
	for _, r := range replicas {
		if _, exists := replicaMap[r.ID]; !exists {
			replicaMap[r.ID] = r
			replicaIDs = append(replicaIDs, r.ID)
		}
	}

	// Process in batches to avoid SQLite limits on variables in IN clause (limit is 999)
	batchSize := 900
	for i := 0; i < len(replicaIDs); i += batchSize {
		end := i + batchSize
		if end > len(replicaIDs) {
			end = len(replicaIDs)
		}

		batch := replicaIDs[i:end]

		args := make([]interface{}, len(batch))
		for j, id := range batch {
			args[j] = id
		}

		// Avoid allocating a string slice and joining it for placeholders
		placeholders := strings.Repeat("?,", len(batch))
		if len(placeholders) > 0 {
			placeholders = placeholders[:len(placeholders)-1]
		}

		query := fmt.Sprintf(`
		SELECT id, replica_id, fragment_number, fragments_total, size, native_fragment_id
		FROM replica_fragments
		WHERE replica_id IN (%s)
		ORDER BY replica_id, fragment_number ASC
		`, placeholders)

		// Use db.conn.Query directly to avoid caching dynamic queries in db.stmtCache, preventing a prepared statement memory leak.
		rows, err := db.conn.Query(query, args...)
		if err != nil {
			return err
		}

		for rows.Next() {
			f := &model.ReplicaFragment{}
			err := rows.Scan(&f.ID, &f.ReplicaID, &f.FragmentNumber, &f.FragmentsTotal, &f.Size, &f.NativeFragmentID)
			if err != nil {
				rows.Close()
				return err
			}
			if r, ok := replicaMap[f.ReplicaID]; ok {
				r.Fragments = append(r.Fragments, f)
			}
		}

		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	return nil
}

// GetActiveFilesByPathPrefix returns active files whose path starts with prefix, including their replicas.
func (db *DB) GetActiveFilesByPathPrefix(prefix string) ([]*model.File, error) {
	pattern := prefix + "%"
	queryFiles := `
	SELECT id, path, name, size, google_drive_md5, mod_time, status
	FROM files
	WHERE status = 'active' AND (path LIKE ? OR path LIKE ?)
	`

	rows, err := db.query(queryFiles, pattern, prefix+`\%`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fileMap := make(map[string]*model.File)
	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var modTime int64
		if err := rows.Scan(&file.ID, &file.Path, &file.Name, &file.Size, &file.GoogleDriveMD5, &modTime, &file.Status); err != nil {
			return nil, err
		}
		file.ModTime = time.Unix(modTime, 0)
		fileMap[file.ID] = file
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return files, nil
	}

	// Load replicas for these files
	placeholders := make([]string, len(files))
	args := make([]interface{}, len(files))
	for i, f := range files {
		placeholders[i] = "?"
		args[i] = f.ID
	}
	repQuery := `
	SELECT id, file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE file_id IN (` + strings.Join(placeholders, ",") + `)`

	repRows, err := db.query(repQuery, args...)
	if err != nil {
		return nil, err
	}
	defer repRows.Close()
	for repRows.Next() {
		r := &model.Replica{}
		var rFileID, rPath, rName, rProvider, rAccountID, rNativeID, rNativeHash, rStatus string
		var rSize, rModTime int64
		var rFragmented bool
		var rOwner sql.NullString
		if err := repRows.Scan(&r.ID, &rFileID, &rPath, &rName, &rSize, &rProvider, &rAccountID, &rNativeID, &rNativeHash, &rModTime, &rStatus, &rFragmented, &rOwner); err != nil {
			return nil, err
		}
		r.FileID = rFileID
		r.Path = rPath
		r.Name = rName
		r.Size = rSize
		r.Provider = model.Provider(rProvider)
		r.AccountID = rAccountID
		r.NativeID = rNativeID
		r.NativeHash = rNativeHash
		r.ModTime = time.Unix(rModTime, 0)
		r.Status = rStatus
		r.Fragmented = rFragmented
		if rOwner.Valid {
			r.Owner = rOwner.String
		}
		if file, ok := fileMap[rFileID]; ok {
			file.Replicas = append(file.Replicas, r)
		}
	}
	return files, repRows.Err()
}

// GetFilesByStatus returns all files with a specific status
func (db *DB) GetFilesByStatus(status string) ([]*model.File, error) {
	queryFiles := `
	SELECT id, path, name, size, google_drive_md5, mod_time, status
	FROM files
	WHERE status = ?
	`

	rows, err := db.query(queryFiles, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fileMap := make(map[string]*model.File)
	var files []*model.File

	for rows.Next() {
		file := &model.File{}
		var modTime int64
		if err := rows.Scan(&file.ID, &file.Path, &file.Name, &file.Size, &file.GoogleDriveMD5, &modTime, &file.Status); err != nil {
			return nil, err
		}
		file.ModTime = time.Unix(modTime, 0)
		fileMap[file.ID] = file
		files = append(files, file)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return files, nil
	}

	var allReplicas []*model.Replica

	queryReplicas := `
	SELECT r.id, r.file_id, r.path, r.name, r.size, r.provider, r.account_id, r.native_id, r.native_hash, r.mod_time, r.status, r.fragmented, r.owner
	FROM replicas r
	JOIN files f ON r.file_id = f.id
	WHERE f.status = ?
	`
	repRows, err := db.query(queryReplicas, status)
	if err != nil {
		return nil, err
	}

	for repRows.Next() {
		r := &model.Replica{}
		var rFileID, rPath, rName, rProvider, rAccountID, rNativeID, rNativeHash, rStatus string
		var rSize, rModTime int64
		var rFragmented bool
		var rOwner sql.NullString

		if err := repRows.Scan(
			&r.ID, &rFileID, &rPath, &rName, &rSize, &rProvider, &rAccountID, &rNativeID, &rNativeHash, &rModTime, &rStatus, &rFragmented, &rOwner,
		); err != nil {
			repRows.Close()
			return nil, err
		}

		r.FileID = rFileID
		r.Path = rPath
		r.Name = rName
		r.Size = rSize
		r.Provider = model.Provider(rProvider)
		r.AccountID = rAccountID
		r.NativeID = rNativeID
		r.NativeHash = rNativeHash
		r.ModTime = time.Unix(rModTime, 0)
		r.Status = rStatus
		r.Fragmented = rFragmented
		if rOwner.Valid {
			r.Owner = rOwner.String
		}

		if file, ok := fileMap[r.FileID]; ok {
			file.Replicas = append(file.Replicas, r)
			allReplicas = append(allReplicas, r)
		}
	}

	if err := repRows.Err(); err != nil {
		repRows.Close()
		return nil, err
	}
	repRows.Close()

	// Load fragments for fragmented replicas in batch
	fragmented := make([]*model.Replica, 0, len(allReplicas))
	for _, r := range allReplicas {
		if r.Fragmented {
			fragmented = append(fragmented, r)
		}
	}
	if len(fragmented) > 0 {
		if err := db.batchLoadFragments(fragmented); err != nil {
			return nil, err
		}
	}

	return files, nil
}

// GetAllFilesAcrossProviders returns all active files with their active replicas
func (db *DB) GetAllFilesAcrossProviders() ([]*model.File, error) {
	queryFiles := `
	SELECT id, path, name, size, google_drive_md5, mod_time, status
	FROM files
	WHERE status = 'active'
	`

	rows, err := db.query(queryFiles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fileMap := make(map[string]*model.File)
	var files []*model.File

	for rows.Next() {
		file := &model.File{}
		var modTime int64
		if err := rows.Scan(&file.ID, &file.Path, &file.Name, &file.Size, &file.GoogleDriveMD5, &modTime, &file.Status); err != nil {
			return nil, err
		}
		file.ModTime = time.Unix(modTime, 0)
		fileMap[file.ID] = file
		files = append(files, file)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(files) == 0 {
		return files, nil
	}

	var allReplicas []*model.Replica

	queryReplicas := `
	SELECT r.id, r.file_id, r.path, r.name, r.size, r.provider, r.account_id, r.native_id, r.native_hash, r.mod_time, r.status, r.fragmented, r.owner
	FROM replicas r
	JOIN files f ON r.file_id = f.id
	WHERE f.status = 'active' AND r.status = 'active'
	`
	repRows, err := db.query(queryReplicas)
	if err != nil {
		return nil, err
	}

	for repRows.Next() {
		r := &model.Replica{}
		var rFileID, rPath, rName, rProvider, rAccountID, rNativeID, rNativeHash, rStatus string
		var rSize, rModTime int64
		var rFragmented bool
		var rOwner sql.NullString

		if err := repRows.Scan(
			&r.ID, &rFileID, &rPath, &rName, &rSize, &rProvider, &rAccountID, &rNativeID, &rNativeHash, &rModTime, &rStatus, &rFragmented, &rOwner,
		); err != nil {
			repRows.Close()
			return nil, err
		}

		r.FileID = rFileID
		r.Path = rPath
		r.Name = rName
		r.Size = rSize
		r.Provider = model.Provider(rProvider)
		r.AccountID = rAccountID
		r.NativeID = rNativeID
		r.NativeHash = rNativeHash
		r.ModTime = time.Unix(rModTime, 0)
		r.Status = rStatus
		r.Fragmented = rFragmented
		if rOwner.Valid {
			r.Owner = rOwner.String
		}

		if file, ok := fileMap[r.FileID]; ok {
			file.Replicas = append(file.Replicas, r)
			allReplicas = append(allReplicas, r)
		}
	}

	if err := repRows.Err(); err != nil {
		repRows.Close()
		return nil, err
	}
	repRows.Close()

	// Load fragments for fragmented replicas in batch
	fragmented := make([]*model.Replica, 0, len(allReplicas))
	for _, r := range allReplicas {
		if r.Fragmented {
			fragmented = append(fragmented, r)
		}
	}
	if len(fragmented) > 0 {
		if err := db.batchLoadFragments(fragmented); err != nil {
			return nil, err
		}
	}

	return files, nil
}

// GetReplicas returns all replicas for a file
func (db *DB) GetReplicas(fileID string) ([]*model.Replica, error) {
	query := `
	SELECT id, file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE file_id = ?
	`
	rows, err := db.query(query, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var replicas []*model.Replica
	for rows.Next() {
		r := &model.Replica{}
		var providerStr string
		var modTime int64
		var owner sql.NullString
		err := rows.Scan(&r.ID, &r.FileID, &r.Path, &r.Name, &r.Size,
			&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented, &owner)
		if err != nil {
			return nil, err
		}
		if owner.Valid {
			r.Owner = owner.String
		}
		r.Provider = model.Provider(providerStr)
		r.ModTime = time.Unix(modTime, 0)
		replicas = append(replicas, r)
	}

	// Load fragments for fragmented replicas
	fragmented := make([]*model.Replica, 0, len(replicas))
	for _, r := range replicas {
		if r.Fragmented {
			fragmented = append(fragmented, r)
		}
	}
	if len(fragmented) > 0 {
		if err := db.batchLoadFragments(fragmented); err != nil {
			return nil, fmt.Errorf("failed to batch load fragments: %w", err)
		}
	}

	return replicas, nil
}

// GetReplicaFragments returns all fragments for a replica
func (db *DB) GetReplicaFragments(replicaID int64) ([]*model.ReplicaFragment, error) {
	query := `
	SELECT id, replica_id, fragment_number, fragments_total, size, native_fragment_id
	FROM replica_fragments
	WHERE replica_id = ?
	ORDER BY fragment_number ASC
	`
	rows, err := db.query(query, replicaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fragments []*model.ReplicaFragment
	for rows.Next() {
		f := &model.ReplicaFragment{}
		err := rows.Scan(&f.ID, &f.ReplicaID, &f.FragmentNumber, &f.FragmentsTotal, &f.Size, &f.NativeFragmentID)
		if err != nil {
			return nil, err
		}
		fragments = append(fragments, f)
	}
	return fragments, nil
}

// GetReplicasByAccount returns all replicas for a specific account
func (db *DB) GetReplicasByAccount(provider model.Provider, accountID string) ([]*model.Replica, error) {
	query := `
	SELECT id, file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE provider = ? AND account_id = ?
	`
	rows, err := db.query(query, string(provider), accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var replicas []*model.Replica
	for rows.Next() {
		r := &model.Replica{}
		var providerStr string
		var modTime int64
		var owner sql.NullString
		err := rows.Scan(&r.ID, &r.FileID, &r.Path, &r.Name, &r.Size,
			&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented, &owner)
		if err != nil {
			return nil, err
		}
		if owner.Valid {
			r.Owner = owner.String
		}
		r.Provider = model.Provider(providerStr)
		r.ModTime = time.Unix(modTime, 0)
		replicas = append(replicas, r)
	}
	return replicas, nil
}

// GetReplicaByNativeID returns a replica by its native ID and provider
func (db *DB) GetReplicaByNativeID(provider model.Provider, nativeID string) (*model.Replica, error) {
	query := `
	SELECT id, file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE provider = ? AND native_id = ?
	`
	r := &model.Replica{}
	var providerStr string
	var modTime int64
	var owner sql.NullString
	err := db.queryRow(query, string(provider), nativeID).Scan(
		&r.ID, &r.FileID, &r.Path, &r.Name, &r.Size,
		&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented, &owner,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if owner.Valid {
		r.Owner = owner.String
	}
	r.Provider = model.Provider(providerStr)
	r.ModTime = time.Unix(modTime, 0)
	return r, nil
}

// HasActiveReplicaByNativeID checks if any active replica exists with the given native ID and provider.
// This is useful for shared files where multiple replicas (some deleted) may share the same NativeID.
func (db *DB) HasActiveReplicaByNativeID(provider model.Provider, nativeID string) (bool, error) {
	query := `SELECT COUNT(*) FROM replicas WHERE provider = ? AND native_id = ? AND status = 'active'`
	var count int
	err := db.queryRow(query, string(provider), nativeID).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetReplicaByNativeFragmentID returns the parent replica of a fragment by the fragment's native ID
func (db *DB) GetReplicaByNativeFragmentID(nativeFragmentID string) (*model.Replica, error) {
	// Join with fragments
	query := `
	SELECT r.id, r.file_id, r.path, r.name, r.size, r.provider, r.account_id, r.native_id, r.native_hash, r.mod_time, r.status, r.fragmented, r.owner
	FROM replicas r
	JOIN replica_fragments f ON r.id = f.replica_id
	WHERE f.native_fragment_id = ?
	`
	r := &model.Replica{}
	var providerStr string
	var modTime int64
	var owner sql.NullString
	err := db.queryRow(query, nativeFragmentID).Scan(
		&r.ID, &r.FileID, &r.Path, &r.Name, &r.Size,
		&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented, &owner,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if owner.Valid {
		r.Owner = owner.String
	}
	r.Provider = model.Provider(providerStr)
	r.ModTime = time.Unix(modTime, 0)
	return r, nil
}

// DeleteFile deletes a file from the database
func (db *DB) DeleteFile(id string) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := "DELETE FROM files WHERE id = ?"
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare delete statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(id)
		if err != nil {
			return fmt.Errorf("failed to delete file: %w", err)
		}
		return nil
	})
}

// DeleteFolder deletes a folder from the database
func (db *DB) DeleteFolder(id string) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := "DELETE FROM folders WHERE id = ?"
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare delete statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(id)
		if err != nil {
			return fmt.Errorf("failed to delete folder: %w", err)
		}
		return nil
	})
}

// ClearProvider is removed/refactored.
// If valid use case warrants clearing a provider:
// we would delete replicas for that provider.
// But files are provider-independent now.
// For now, let's implement a DeleteReplicasForProvider helper.

// DeleteReplicasForProvider removes all replicas for a specific provider
func (db *DB) DeleteReplicasForProvider(provider model.Provider) error {
	return db.WithTx(func(tx *sql.Tx) error {
		stmt, err := db.txStmt(tx, "DELETE FROM replicas WHERE provider = ?")
		if err != nil {
			return fmt.Errorf("failed to prepare delete statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(string(provider))
		if err != nil {
			return fmt.Errorf("failed to delete replicas for provider: %w", err)
		}
		return nil
	})
}

// DeleteReplica removes a specific replica by ID
func (db *DB) DeleteReplica(id int64) error {
	return db.WithTx(func(tx *sql.Tx) error {
		stmt, err := db.txStmt(tx, "DELETE FROM replicas WHERE id = ?")
		if err != nil {
			return fmt.Errorf("failed to prepare delete statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(id)
		if err != nil {
			return fmt.Errorf("failed to delete replica: %w", err)
		}
		return nil
	})
}

// DeleteStaleReplicasByNativeID marks as deleted all replicas pointing to a stale native_id
// after a file has been transferred/moved. This prevents 404 errors when trying to download
// from replicas that reference a file that no longer exists.
func (db *DB) DeleteStaleReplicasByNativeID(provider model.Provider, oldNativeID string, excludeReplicaID int64) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		UPDATE replicas
		SET status = 'deleted'
		WHERE provider = ? AND native_id = ? AND id != ? AND status = 'active'
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(string(provider), oldNativeID, excludeReplicaID)
		if err != nil {
			return fmt.Errorf("failed to delete stale replicas: %w", err)
		}
		return nil
	})
}

// UpdateSoftDeletedFileStatus marks files as softdeleted or active based on replica locations
// Priority: Google provider state takes precedence when replicas disagree
// Only considers recently scanned replicas (last_seen_at >= query start time)
func (db *DB) UpdateSoftDeletedFileStatus(scanStartTime time.Time) error {
	return db.WithTx(func(tx *sql.Tx) error {
		minTimestamp := scanStartTime.Unix()

		softDeletedPattern := auxFolderName + "/soft-deleted"
		softDeletedPatternWin := auxFolderName + `\soft-deleted`

		// Single-pass update: Determine the correct status based on current replica locations
		updateQuery := fmt.Sprintf(`
		WITH ReplicaAgg AS (
			SELECT file_id,
				SUM(CASE WHEN provider = 'google' AND path NOT LIKE '%%%s%%' AND path NOT LIKE '%%%s%%' THEN 1 ELSE 0 END) as active_google,
				SUM(CASE WHEN provider = 'google' THEN 1 ELSE 0 END) as total_google,
				SUM(CASE WHEN path NOT LIKE '%%%s%%' AND path NOT LIKE '%%%s%%' THEN 1 ELSE 0 END) as active_any,
				COUNT(*) as total_any
			FROM replicas
			WHERE status = 'active' AND last_seen_at >= ?
			GROUP BY file_id
		)
		UPDATE files
		SET status = CASE
			WHEN r.active_google > 0 THEN 'active'
			WHEN r.total_google > 0 AND r.active_google = 0 THEN 'soft-deleted'
			WHEN r.total_google = 0 AND r.active_any > 0 THEN 'active'
			WHEN r.total_google = 0 AND r.total_any > 0 AND r.active_any = 0 THEN 'soft-deleted'
			ELSE files.status
		END
		FROM ReplicaAgg r
		WHERE files.id = r.file_id
		AND files.status != CASE
			WHEN r.active_google > 0 THEN 'active'
			WHEN r.total_google > 0 AND r.active_google = 0 THEN 'soft-deleted'
			WHEN r.total_google = 0 AND r.active_any > 0 THEN 'active'
			WHEN r.total_google = 0 AND r.total_any > 0 AND r.active_any = 0 THEN 'soft-deleted'
			ELSE files.status
		END
		`, softDeletedPattern, softDeletedPatternWin, softDeletedPattern, softDeletedPatternWin)

		updateStmt, err := db.txStmt(tx, updateQuery)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer updateStmt.Close()
		if _, err := updateStmt.Exec(minTimestamp); err != nil {
			return fmt.Errorf("failed to update soft-deleted status: %w", err)
		}

		// Second pass: catch files that remain 'soft-deleted' due to file_id linkage issues
		// by checking Google replicas against canonical file identity.
		fallbackQuery := fmt.Sprintf(`
		WITH LatestActive AS (
			SELECT
				r.file_id,
				r.path,
				ROW_NUMBER() OVER(
					PARTITION BY r.file_id
					ORDER BY r.mod_time DESC
				) as rn
			FROM replicas r
			WHERE r.status = 'active'
			AND r.provider = 'google'
			AND r.file_id IS NOT NULL
			AND r.file_id != ''
			AND r.last_seen_at >= ?
			AND r.path NOT LIKE '%%%s%%'
			AND r.path NOT LIKE '%%%s%%'
		)
		UPDATE files
		SET status = 'active',
			path = COALESCE(la.path, files.path)
		FROM LatestActive la
		WHERE files.id = la.file_id
		AND la.rn = 1
		AND files.status = 'soft-deleted'
		`, softDeletedPattern, softDeletedPatternWin)

		fallbackStmt, err := db.txStmt(tx, fallbackQuery)
		if err != nil {
			return fmt.Errorf("failed to prepare fallback statement: %w", err)
		}
		defer fallbackStmt.Close()
		if _, err := fallbackStmt.Exec(minTimestamp); err != nil {
			return fmt.Errorf("failed to update soft-deleted status (fallback): %w", err)
		}

		return nil
	})
}

// DBExists checks if the database file exists
func DBExists() bool {
	dbPath := GetDBPath()
	_, err := os.Stat(dbPath)
	return err == nil
}

// CreateDB creates a new encrypted database
func CreateDB(masterPassword string) error {
	dbPath := GetDBPath()

	// Create database file with SQLCipher encryption using _pragma_key parameter
	// This ensures the key is set via PRAGMA key before any DB operations
	// Using WAL and busy_timeout to support concurrent reads/writes
	connStr := fmt.Sprintf("file:%s?_pragma_key=%s&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000", dbPath, url.QueryEscape(masterPassword))
	conn, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Create a test table to initialize the encrypted database
	// This forces SQLCipher to write the encrypted header
	if _, err := conn.Exec("CREATE TABLE IF NOT EXISTS _init (id INTEGER PRIMARY KEY)"); err != nil {
		return fmt.Errorf("failed to initialize encrypted database: %w", err)
	}

	// Insert a test row to ensure the database is properly written to disk
	if _, err := conn.Exec("INSERT INTO _init (id) VALUES (1);"); err != nil {
		return fmt.Errorf("failed to write to encrypted database: %w", err)
	}

	return nil
}

// GetFileByPath retrieves a file by its path
func (db *DB) GetFileByPath(path string) (*model.File, error) {
	query := `
	SELECT id, path, name, size, google_drive_md5, mod_time, status
	FROM files
	WHERE path = ?
	`
	row := db.queryRow(query, path)

	file := &model.File{}
	var modTime int64
	err := row.Scan(
		&file.ID, &file.Path, &file.Name, &file.Size,
		&file.GoogleDriveMD5, &modTime, &file.Status,
	)
	if err == sql.ErrNoRows {
		return nil, nil // Return nil if not found
	}
	if err != nil {
		return nil, fmt.Errorf("failed to scan file: %w", err)
	}
	file.ModTime = time.Unix(modTime, 0)

	// Get Replicas
	replicas, err := db.GetReplicas(file.ID)
	if err != nil {
		return nil, err
	}
	file.Replicas = replicas

	return file, nil
}

// GetFileByID retrieves a file by its ID
func (db *DB) GetFileByID(id string) (*model.File, error) {
	query := `
	SELECT id, path, name, size, google_drive_md5, mod_time, status
	FROM files
	WHERE id = ?
	`

	file := &model.File{}
	var modTime int64
	err := db.queryRow(query, id).Scan(
		&file.ID, &file.Path, &file.Name, &file.Size, &file.GoogleDriveMD5, &modTime, &file.Status,
	)
	if err != nil {
		return nil, err
	}
	file.ModTime = time.Unix(modTime, 0)

	// Load Replicas
	replicas, err := db.GetReplicas(file.ID)
	if err != nil {
		return nil, err
	}
	file.Replicas = replicas

	return file, nil
}

// UpdateFile updates a file record in the database
func (db *DB) UpdateFile(file *model.File) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		UPDATE files 
		SET path = ?, name = ?, size = ?, google_drive_md5 = ?, mod_time = ?, status = ?
		WHERE id = ?
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare update file statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(
			file.Path, file.Name, file.Size, file.GoogleDriveMD5, file.ModTime.Unix(), file.Status, file.ID)
		if err != nil {
			return fmt.Errorf("failed to update file: %w", err)
		}
		return nil
	})
}

// UpdateReplica updates a replica record
func (db *DB) UpdateReplica(replica *model.Replica) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		UPDATE replicas SET
			file_id = ?, path = ?, name = ?, size = ?,
			provider = ?, account_id = ?, native_id = ?, native_hash = ?,
			mod_time = ?, status = ?, fragmented = ?, owner = ?
		WHERE id = ?
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare update replica statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(
			replica.FileID, replica.Path, replica.Name, replica.Size,
			string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
			replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner, replica.ID)
		if err != nil {
			return fmt.Errorf("failed to update replica: %w", err)
		}
		return nil
	})
}

// UpdateFileStatus updates the status of a file
func (db *DB) UpdateFileStatus(id string, status string) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := "UPDATE files SET status = ? WHERE id = ?"
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(status, id)
		if err != nil {
			return fmt.Errorf("failed to update file status: %w", err)
		}
		return nil
	})
}

// UpdateFileModTime updates the modification time of a file
func (db *DB) UpdateFileModTime(id string, modTime time.Time) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := "UPDATE files SET mod_time = ? WHERE id = ?"
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(modTime.Unix(), id)
		if err != nil {
			return fmt.Errorf("failed to update file mod time: %w", err)
		}
		return nil
	})
}

// UpdateReplicaFileID updates the file_id of a replica
func (db *DB) UpdateReplicaFileID(replicaID int64, fileID string) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := "UPDATE replicas SET file_id = ? WHERE id = ?"
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(fileID, replicaID)
		if err != nil {
			return fmt.Errorf("failed to update replica file ID: %w", err)
		}
		return nil
	})
}

// GetReplicasWithNullFileID returns all replicas without a file_id
func (db *DB) GetReplicasWithNullFileID() ([]*model.Replica, error) {
	query := `
	SELECT id, file_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE file_id IS NULL
	`
	rows, err := db.query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var replicas []*model.Replica
	for rows.Next() {
		r := &model.Replica{}
		var providerStr string
		var modTime int64
		var fileID sql.NullString
		var owner sql.NullString
		err := rows.Scan(&r.ID, &fileID, &r.Path, &r.Name, &r.Size,
			&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented, &owner)
		if err != nil {
			return nil, err
		}
		r.Provider = model.Provider(providerStr)
		r.ModTime = time.Unix(modTime, 0)
		if fileID.Valid {
			r.FileID = fileID.String
		}
		if owner.Valid {
			r.Owner = owner.String
		}
		replicas = append(replicas, r)
	}
	return replicas, nil
}

// LinkOrphanedReplicas links orphaned replicas to existing files, preferring canonical Google Drive MD5
// identity and otherwise falling back to logical path.
func (db *DB) LinkOrphanedReplicas() error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		WITH FileMap AS (
			SELECT id, google_drive_md5, path
			FROM files
		),
		ReplicaMatch AS (
			SELECT r.id AS replica_id,
				COALESCE(
					(
						SELECT fm.id
						FROM FileMap fm
						WHERE fm.google_drive_md5 != ''
						AND LOWER(r.provider) = 'google'
						AND r.native_hash = fm.google_drive_md5
						LIMIT 1
					),
					(
						SELECT fm.id
						FROM FileMap fm
						WHERE (fm.google_drive_md5 IS NULL OR fm.google_drive_md5 = '')
						AND fm.path = r.path
						LIMIT 1
					)
				) AS file_id
			FROM replicas r
			WHERE r.file_id IS NULL OR r.file_id = ''
		)
		UPDATE replicas
		SET file_id = rm.file_id
		FROM ReplicaMatch rm
		WHERE replicas.id = rm.replica_id
		AND rm.file_id IS NOT NULL
		AND rm.file_id != ''
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec()
		if err != nil {
			return fmt.Errorf("failed to link orphaned replicas: %w", err)
		}
		return nil
	})
}

// PromoteOrphanedReplicasToFiles creates new logical file records for replicas that still do not match any
// existing file. Established Google identities are grouped by MD5; otherwise path is used.
func (db *DB) PromoteOrphanedReplicasToFiles() error {
	query := `
	SELECT id, path, name, size, mod_time, status, provider, native_hash
	FROM replicas
	WHERE file_id IS NULL OR file_id = ''
	`
	rows, err := db.query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	type Orphan struct {
		ReplicaID      int64
		Path           string
		Name           string
		Size           int64
		ModTime        int64
		Status         string
		Provider       string
		NativeHash     string
		GoogleDriveMD5 string
		GroupingKey    string
	}

	var orphans []Orphan
	for rows.Next() {
		var o Orphan
		if err := rows.Scan(&o.ReplicaID, &o.Path, &o.Name, &o.Size, &o.ModTime, &o.Status, &o.Provider, &o.NativeHash); err != nil {
			return err
		}
		if strings.EqualFold(o.Provider, string(model.ProviderGoogle)) && o.NativeHash != "" {
			o.GoogleDriveMD5 = o.NativeHash
			o.GroupingKey = "md5:" + o.GoogleDriveMD5
		} else {
			o.GroupingKey = "path:" + o.Path
		}
		orphans = append(orphans, o)
	}
	rows.Close()

	if len(orphans) == 0 {
		return nil
	}

	orphanGroups := make(map[string][]Orphan)
	for _, o := range orphans {
		orphanGroups[o.GroupingKey] = append(orphanGroups[o.GroupingKey], o)
	}

	return db.WithTx(func(tx *sql.Tx) error {
		insertFileStmt, err := db.txStmt(tx, `
			INSERT OR IGNORE INTO files (id, path, name, size, google_drive_md5, mod_time, status)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`)
		if err != nil {
			return err
		}
		defer insertFileStmt.Close()

		updateReplicaStmt, err := db.txStmt(tx, `
			UPDATE replicas SET file_id = ? WHERE id = ?
		`)
		if err != nil {
			return err
		}
		defer updateReplicaStmt.Close()

		for groupingKey, group := range orphanGroups {
			first := group[0]
			newFileID := uuid.New().String()
			googleDriveMD5 := first.GoogleDriveMD5
			_, err := insertFileStmt.Exec(newFileID, first.Path, first.Name, first.Size, googleDriveMD5, first.ModTime, first.Status)
			if err != nil {
				return fmt.Errorf("failed to promote replica group %s: %w", groupingKey, err)
			}

			for _, o := range group {
				_, err = updateReplicaStmt.Exec(newFileID, o.ReplicaID)
				if err != nil {
					return fmt.Errorf("failed to update replica %d: %w", o.ReplicaID, err)
				}
			}
		}

		return nil
	})
}

// UpdateLogicalFilesFromReplicas refreshes logical file metadata from active replicas while keeping Google
// Drive authoritative for canonical path/name and Google MD5. Placeholder replicas are ranked last so
// zero-byte accounting entries cannot overwrite canonical logical metadata when a real replica exists.
func (db *DB) UpdateLogicalFilesFromReplicas() error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		WITH RankedReplicas AS (
			SELECT r.file_id,
				r.size,
				r.mod_time,
				r.name,
				r.path,
				r.native_hash,
				r.provider,
				ROW_NUMBER() OVER (
					PARTITION BY r.file_id
					ORDER BY
						CASE WHEN LOWER(r.provider) = 'google' AND r.native_hash != ? THEN 0 ELSE 1 END,
						CASE WHEN r.native_hash = ? THEN 1 ELSE 0 END,
						r.mod_time DESC,
						CASE WHEN r.path != f.path THEN 0 ELSE 1 END
				) AS rn
			FROM replicas r
			JOIN files f ON f.id = r.file_id
			WHERE r.status = 'active'
		),
		CanonicalReplicas AS (
			SELECT file_id,
				size,
				mod_time,
				name,
				path,
				CASE WHEN LOWER(provider) = 'google' AND native_hash != ? THEN native_hash ELSE NULL END AS google_drive_md5
			FROM RankedReplicas
			WHERE rn = 1
		)
		UPDATE files
		SET
			size = cr.size,
			mod_time = cr.mod_time,
			name = cr.name,
			path = cr.path,
			google_drive_md5 = COALESCE(cr.google_drive_md5, files.google_drive_md5)
		FROM CanonicalReplicas cr
		WHERE files.id = cr.file_id
		AND (
			files.size IS NOT cr.size OR
			files.mod_time IS NOT cr.mod_time OR
			files.name IS NOT cr.name OR
			files.path IS NOT cr.path OR
			(COALESCE(cr.google_drive_md5, files.google_drive_md5) IS NOT files.google_drive_md5)
		)
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(model.NativeHashShortcut, model.NativeHashShortcut, model.NativeHashShortcut)
		if err != nil {
			return fmt.Errorf("failed to update logical files from replicas: %w", err)
		}
		return nil
	})
}

// MarkDeletedReplicas marks replicas as deleted if they weren't seen since the given time
func (db *DB) MarkDeletedReplicas(startTime time.Time) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `
		UPDATE replicas
		SET status = 'deleted'
		WHERE last_seen_at < ? AND status != 'deleted'
		`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(startTime.Unix())
		if err != nil {
			return fmt.Errorf("failed to mark deleted replicas: %w", err)
		}
		return nil
	})
}

// GetProviderUsage returns the total size of active files for a provider
func (db *DB) GetProviderUsage(provider model.Provider) (int64, error) {
	// Only count files where the account is the owner (or owner is unknown/empty)
	// This prevents double counting of shared files across multiple accounts
	query := `
	SELECT COALESCE(SUM(size), 0)
	FROM replicas
	WHERE provider = ? AND status = 'active'
	AND (owner = '' OR owner IS NULL OR LOWER(owner) = LOWER(account_id))
	`
	var size int64
	err := db.queryRow(query, provider).Scan(&size)
	if err != nil {
		return 0, err
	}
	return size, nil
}

// CreateSyncRun inserts a new sync run and returns its ID
func (db *DB) CreateSyncRun(safeMode bool) (int64, error) {
	var id int64
	err := db.WithTx(func(tx *sql.Tx) error {
		query := `INSERT INTO sync_runs (started_at, safe_mode) VALUES (?, ?)`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		res, err := stmt.Exec(time.Now().Unix(), safeMode)
		if err != nil {
			return fmt.Errorf("failed to create sync run: %w", err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// GetIncompleteSyncRun returns the most recent sync run that has not completed and was started within the last 24 hours, or nil
func (db *DB) GetIncompleteSyncRun() (*model.SyncRun, error) {
	twentyFourHoursAgo := time.Now().Add(-24 * time.Hour).Unix()
	query := `SELECT id, started_at, last_completed_step, safe_mode FROM sync_runs WHERE completed_at IS NULL AND started_at > ? ORDER BY id DESC LIMIT 1`
	row := db.queryRow(query, twentyFourHoursAgo)

	var run model.SyncRun
	var startedAt int64
	err := row.Scan(&run.ID, &startedAt, &run.LastCompletedStep, &run.SafeMode)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get incomplete sync run: %w", err)
	}
	run.StartedAt = time.Unix(startedAt, 0)
	return &run, nil
}

// MarkStepCompleted updates the last completed step for a sync run
func (db *DB) MarkStepCompleted(runID int64, step int) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `UPDATE sync_runs SET last_completed_step = ? WHERE id = ?`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(step, runID)
		if err != nil {
			return fmt.Errorf("failed to mark step completed: %w", err)
		}
		return nil
	})
}

// CompleteSyncRun marks a sync run as fully completed
func (db *DB) CompleteSyncRun(runID int64) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `UPDATE sync_runs SET completed_at = ?, last_completed_step = 5 WHERE id = ?`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(time.Now().Unix(), runID)
		if err != nil {
			return fmt.Errorf("failed to complete sync run: %w", err)
		}
		return nil
	})
}

// LogSyncCopy records a successful file copy within a sync run
func (db *DB) LogSyncCopy(runID int64, fileID string, targetProvider string) error {
	return db.WithTx(func(tx *sql.Tx) error {
		query := `INSERT OR IGNORE INTO sync_copy_log (sync_run_id, file_id, target_provider, created_at) VALUES (?, ?, ?, ?)`
		stmt, err := db.txStmt(tx, query)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt.Close()
		_, err = stmt.Exec(runID, fileID, targetProvider, time.Now().Unix())
		if err != nil {
			return fmt.Errorf("failed to log sync copy: %w", err)
		}
		return nil
	})
}

// IsSyncCopyDone checks whether a file has already been copied to a provider in this sync run
func (db *DB) IsSyncCopyDone(runID int64, fileID string, targetProvider string) (bool, error) {
	query := `SELECT 1 FROM sync_copy_log WHERE sync_run_id = ? AND file_id = ? AND target_provider = ? LIMIT 1`
	var exists int
	err := db.queryRow(query, runID, fileID, targetProvider).Scan(&exists)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// BatchCheckSyncCopyDone checks which (file_id, target_provider) pairs have already been copied in this sync run.
// Returns a set of "fileID\x00provider" keys for O(1) lookup.
func (db *DB) BatchCheckSyncCopyDone(runID int64) (map[string]bool, error) {
	query := `SELECT file_id, target_provider FROM sync_copy_log WHERE sync_run_id = ?`
	rows, err := db.query(query, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var fileID, provider string
		if err := rows.Scan(&fileID, &provider); err != nil {
			return nil, err
		}
		result[fileID+"\x00"+provider] = true
	}
	return result, rows.Err()
}

// CleanupOldSyncRuns deletes completed sync runs beyond the most recent keepLast
func (db *DB) CleanupOldSyncRuns(keepLast int) error {
	return db.WithTx(func(tx *sql.Tx) error {
		delCopyLog := `
		DELETE FROM sync_copy_log WHERE sync_run_id IN (
			SELECT id FROM sync_runs WHERE completed_at IS NOT NULL
			ORDER BY id DESC LIMIT -1 OFFSET ?
		)`
		stmt1, err := db.txStmt(tx, delCopyLog)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt1.Close()
		if _, err := stmt1.Exec(keepLast); err != nil {
			return fmt.Errorf("failed to delete old sync copy logs: %w", err)
		}

		delRuns := `
		DELETE FROM sync_runs WHERE completed_at IS NOT NULL
			AND id NOT IN (
				SELECT id FROM sync_runs WHERE completed_at IS NOT NULL
				ORDER BY id DESC LIMIT ?
			)`
		stmt2, err := db.txStmt(tx, delRuns)
		if err != nil {
			return fmt.Errorf("failed to prepare statement: %w", err)
		}
		defer stmt2.Close()
		if _, err := stmt2.Exec(keepLast); err != nil {
			return fmt.Errorf("failed to delete old sync runs: %w", err)
		}

		return nil
	})
}
