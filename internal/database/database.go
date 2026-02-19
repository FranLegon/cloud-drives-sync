package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/google/uuid"
	_ "github.com/mutecomm/go-sqlcipher/v4"
)

const (
	DBFileName = "metadata.db"
	DBUser     = "owner"
)

// DB represents the database connection
type DB struct {
	conn *sql.DB
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
	connStr := fmt.Sprintf("file:%s?_pragma_key=%s", dbPath, url.QueryEscape(masterPassword))

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

// Reset clears all data from the database
func (db *DB) Reset() error {
	tables := []string{"replica_fragments", "replicas", "files", "folders"}
	for _, table := range tables {
		_, err := db.conn.Exec(fmt.Sprintf("DELETE FROM %s", table))
		if err != nil {
			// Ignore if table doesn't exist
			continue
		}
	}
	return nil
}

// Close closes the database connection
func (db *DB) Close() error {
	if db.conn != nil {
		return db.conn.Close()
	}
	return nil
}

// Initialize creates the database schema
func (db *DB) Initialize() error {
	schema := `
	CREATE TABLE IF NOT EXISTS files (
		id TEXT PRIMARY KEY,
		path TEXT NOT NULL,
		name TEXT NOT NULL,
		size INTEGER NOT NULL,
		calculated_id TEXT,
		mod_time INTEGER NOT NULL,
		status TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);
	CREATE INDEX IF NOT EXISTS idx_files_calculated_id ON files(calculated_id);
	CREATE INDEX IF NOT EXISTS idx_files_status ON files(status);

	CREATE TABLE IF NOT EXISTS replicas (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_id TEXT,
		calculated_id TEXT,
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
	CREATE INDEX IF NOT EXISTS idx_replicas_calculated_id ON replicas(calculated_id);
	CREATE INDEX IF NOT EXISTS idx_replicas_provider ON replicas(provider);
	CREATE INDEX IF NOT EXISTS idx_replicas_account_id ON replicas(account_id);
	CREATE INDEX IF NOT EXISTS idx_replicas_native_id ON replicas(native_id);
	CREATE INDEX IF NOT EXISTS idx_replicas_status ON replicas(status);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_replicas_unique ON replicas(provider, account_id, native_id);

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
	`

	if _, err := db.conn.Exec(schema); err != nil {
		return err
	}

	// Migrations
	_, _ = db.conn.Exec("ALTER TABLE replicas ADD COLUMN last_seen_at INTEGER DEFAULT 0")
	_, _ = db.conn.Exec("ALTER TABLE replicas ADD COLUMN owner TEXT DEFAULT ''")

	return nil
}

// InsertFile inserts a file record into the database
func (db *DB) InsertFile(file *model.File) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Insert into files table
	fileQuery := `
	INSERT OR REPLACE INTO files (
		id, path, name, size, calculated_id, mod_time, status
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err = tx.Exec(fileQuery,
		file.ID, file.Path, file.Name, file.Size, file.CalculatedID, file.ModTime.Unix(), file.Status)
	if err != nil {
		return fmt.Errorf("failed to insert file: %w", err)
	}

	// Insert replicas
	for _, replica := range file.Replicas {
		replicaQuery := `
		INSERT OR REPLACE INTO replicas (
			file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`
		_, err := tx.Exec(replicaQuery,
			file.ID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
			string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
			replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner)
		if err != nil {
			return fmt.Errorf("failed to insert replica: %w", err)
		}

		// Note: Fragments should be inserted separately via InsertReplicaFragment
		// This is because fragments are associated with replicas, not files
	}

	return tx.Commit()
}

// BatchInsertFiles inserts multiple files (replicas and fragments) in a single transaction
func (db *DB) BatchInsertFiles(files []*model.File) error {
	if len(files) == 0 {
		return nil
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Upsert replicas using ON CONFLICT logic to preserve file_id if it exists.
	// We rely on the unique index (provider, account_id, native_id).
	// usage of RETURNING id requires SQLite 3.35+
	now := time.Now().Unix()
	replicaQuery := `
		INSERT INTO replicas (
			calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, file_id, last_seen_at, owner
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)
		ON CONFLICT(provider, account_id, native_id) DO UPDATE SET
			calculated_id=excluded.calculated_id,
			path=excluded.path,
			name=excluded.name,
			size=excluded.size,
			native_hash=excluded.native_hash,
			mod_time=excluded.mod_time,
			status=excluded.status,
			fragmented=excluded.fragmented,
			last_seen_at=excluded.last_seen_at,
			owner=excluded.owner
	`
	replicaStmt, err := tx.Prepare(replicaQuery)
	if err != nil {
		return err
	}
	defer replicaStmt.Close()

	// Prepare ID lookup statement
	idStmt, err := tx.Prepare(`SELECT id FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ?`)
	if err != nil {
		return err
	}
	defer idStmt.Close()

	// Prepare fragment statements
	deleteFragmentsStmt, err := tx.Prepare(`DELETE FROM replica_fragments WHERE replica_id = ?`)
	if err != nil {
		return err
	}
	defer deleteFragmentsStmt.Close()

	fragmentStmt, err := tx.Prepare(`
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
			_, err := replicaStmt.Exec(
				replica.CalculatedID, replica.Path, replica.Name, replica.Size,
				string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
				replica.ModTime.Unix(), replica.Status, replica.Fragmented, now, replica.Owner)

			if err != nil {
				return fmt.Errorf("failed to upsert replica: %w", err)
			}

			var replicaID int64
			err = idStmt.QueryRow(string(replica.Provider), replica.AccountID, replica.NativeID).Scan(&replicaID)
			if err != nil {
				return fmt.Errorf("failed to get replica ID: %w", err)
			}

			if len(replica.Fragments) > 0 {
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

	return tx.Commit()
}

// UpdateReplicaOwner updates the owner (account_id) of a replica.
// This is used during FreeMain when ownership is transferred.
func (db *DB) UpdateReplicaOwner(provider string, oldAccountID, nativeID, newAccountID string) error {
	// Check if target replica already exists to avoid UNIQUE constraint violation
	var exists int
	checkQuery := `SELECT 1 FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ?`
	err := db.conn.QueryRow(checkQuery, provider, newAccountID, nativeID).Scan(&exists)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("failed to check existing replica: %w", err)
	}

	if exists == 1 {
		// Target already exists, so we just remove the old one to reflect the move/change
		// (The new owner is already tracked, so we don't need to update the old record to it)
		delQuery := `DELETE FROM replicas WHERE provider = ? AND account_id = ? AND native_id = ?`
		if _, err := db.conn.Exec(delQuery, provider, oldAccountID, nativeID); err != nil {
			return fmt.Errorf("failed to delete old replica: %w", err)
		}
		return nil
	}

	query := `
		UPDATE replicas
		SET account_id = ?
		WHERE provider = ? AND account_id = ? AND native_id = ?
	`
	res, err := db.conn.Exec(query, newAccountID, provider, oldAccountID, nativeID)
	if err != nil {
		return fmt.Errorf("failed to update replica owner: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("no replica found to update (prov=%s, acc=%s, id=%s)", provider, oldAccountID, nativeID)
	}
	return nil
}

// InsertReplica inserts a replica record into the database
func (db *DB) InsertReplica(replica *model.Replica) error {
	query := `
	INSERT INTO replicas (
		file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	res, err := db.conn.Exec(query,
		replica.FileID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
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
	return nil
}

// UpsertReplica inserts or updates a replica record
func (db *DB) UpsertReplica(replica *model.Replica) error {
	query := `
	INSERT OR REPLACE INTO replicas (
		id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.conn.Exec(query,
		replica.ID, replica.FileID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
		string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
		replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner)
	return err
}

// InsertReplicaFragment inserts a fragment record into the database
func (db *DB) InsertReplicaFragment(fragment *model.ReplicaFragment) error {
	query := `
	INSERT INTO replica_fragments (
		replica_id, fragment_number, fragments_total, size, native_fragment_id
	) VALUES (?, ?, ?, ?, ?)
	`
	res, err := db.conn.Exec(query,
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
}

// InsertFolder inserts a folder record into the database
func (db *DB) InsertFolder(folder *model.Folder) error {
	query := `
	INSERT OR REPLACE INTO folders (
		id, name, path, provider, user_email, user_phone, parent_folder_id, owner_email
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := db.conn.Exec(query,
		folder.ID, folder.Name, folder.Path, string(folder.Provider),
		folder.UserEmail, folder.UserPhone, folder.ParentFolderID, folder.OwnerEmail,
	)
	return err
}

// BatchInsertFolders inserts multiple folders in a single transaction
func (db *DB) BatchInsertFolders(folders []*model.Folder) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
	INSERT OR REPLACE INTO folders (
		id, name, path, provider, user_email, user_phone, parent_folder_id, owner_email
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
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

	return tx.Commit()
}

// GetAllFolders returns all folders from DB
func (db *DB) GetAllFolders() ([]*model.Folder, error) {
	rows, err := db.conn.Query("SELECT id, name, path, provider, user_email, user_phone, parent_folder_id, owner_email FROM folders")
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

// GetFilesByCalculatedID returns all files with a specific calculated_id
func (db *DB) GetFilesByCalculatedID(calculatedID string) ([]*model.File, error) {
	query := `
	SELECT id, path, name, size, calculated_id, mod_time, status
	FROM files
	WHERE calculated_id = ?
	ORDER BY mod_time ASC
	`

	rows, err := db.conn.Query(query, calculatedID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var modTime int64
		err := rows.Scan(
			&file.ID, &file.Path, &file.Name, &file.Size, &file.CalculatedID, &modTime, &file.Status,
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

		files = append(files, file)
	}

	return files, rows.Err()
}

// GetAllFiles returns all files
func (db *DB) GetAllFiles() ([]*model.File, error) {
	query := `
	SELECT id, path, name, size, calculated_id, mod_time, status
	FROM files
	ORDER BY path ASC
	`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var modTime int64
		err := rows.Scan(
			&file.ID, &file.Path, &file.Name, &file.Size, &file.CalculatedID, &modTime, &file.Status,
		)
		if err != nil {
			return nil, err
		}
		file.ModTime = time.Unix(modTime, 0)
		files = append(files, file)
	}

	// Load replicas for each file
	for _, file := range files {
		replicas, err := db.GetReplicas(file.ID)
		if err != nil {
			return nil, err
		}
		file.Replicas = replicas
	}

	return files, rows.Err()
}

// GetFilesByStatus returns all files with a specific status
func (db *DB) GetFilesByStatus(status string) ([]*model.File, error) {
	query := `
	SELECT id, path, name, size, calculated_id, mod_time, status
	FROM files
	WHERE status = ?
	ORDER BY path ASC
	`

	rows, err := db.conn.Query(query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var modTime int64
		err := rows.Scan(
			&file.ID, &file.Path, &file.Name, &file.Size, &file.CalculatedID, &modTime, &file.Status,
		)
		if err != nil {
			return nil, err
		}
		file.ModTime = time.Unix(modTime, 0)
		files = append(files, file)
	}

	// Load replicas for each file
	for _, file := range files {
		replicas, err := db.GetReplicas(file.ID)
		if err != nil {
			return nil, err
		}
		file.Replicas = replicas
	}

	return files, nil
}

// GetAllFilesAcrossProviders returns all files (alias for GetAllFiles for backwards compatibility)
func (db *DB) GetAllFilesAcrossProviders() ([]*model.File, error) {
	return db.GetAllFiles()
}

// GetReplicas returns all replicas for a file
func (db *DB) GetReplicas(fileID string) ([]*model.Replica, error) {
	query := `
	SELECT id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE file_id = ?
	`
	rows, err := db.conn.Query(query, fileID)
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
		err := rows.Scan(&r.ID, &r.FileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
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
	for _, r := range replicas {
		if r.Fragmented {
			fragments, err := db.GetReplicaFragments(r.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to get fragments for replica %d: %w", r.ID, err)
			}
			r.Fragments = fragments
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
	rows, err := db.conn.Query(query, replicaID)
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
	SELECT id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE provider = ? AND account_id = ?
	`
	rows, err := db.conn.Query(query, string(provider), accountID)
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
		err := rows.Scan(&r.ID, &r.FileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
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
	SELECT id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE provider = ? AND native_id = ?
	`
	r := &model.Replica{}
	var providerStr string
	var modTime int64
	var owner sql.NullString
	err := db.conn.QueryRow(query, string(provider), nativeID).Scan(
		&r.ID, &r.FileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
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

// GetReplicaByNativeFragmentID returns the parent replica of a fragment by the fragment's native ID
func (db *DB) GetReplicaByNativeFragmentID(nativeFragmentID string) (*model.Replica, error) {
	// Join with fragments
	query := `
	SELECT r.id, r.file_id, r.calculated_id, r.path, r.name, r.size, r.provider, r.account_id, r.native_id, r.native_hash, r.mod_time, r.status, r.fragmented, r.owner
	FROM replicas r
	JOIN replica_fragments f ON r.id = f.replica_id
	WHERE f.native_fragment_id = ?
	`
	r := &model.Replica{}
	var providerStr string
	var modTime int64
	var owner sql.NullString
	err := db.conn.QueryRow(query, nativeFragmentID).Scan(
		&r.ID, &r.FileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
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
	query := "DELETE FROM files WHERE id = ?"
	_, err := db.conn.Exec(query, id)
	return err
}

// DeleteFolder deletes a folder from the database
func (db *DB) DeleteFolder(id string) error {
	query := "DELETE FROM folders WHERE id = ?"
	_, err := db.conn.Exec(query, id)
	return err
}

// GetDuplicateCalculatedIDs returns calculated_ids that appear more than once
func (db *DB) GetDuplicateCalculatedIDs() ([]string, error) {
	query := `
	SELECT calculated_id
	FROM files
	WHERE calculated_id IS NOT NULL
	GROUP BY calculated_id
	HAVING COUNT(*) > 1
	`
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// ClearProvider is removed/refactored.
// If valid use case warrants clearing a provider:
// we would delete replicas for that provider.
// But files are provider-independent now.
// For now, let's implement a DeleteReplicasForProvider helper.

// DeleteReplicasForProvider removes all replicas for a specific provider
func (db *DB) DeleteReplicasForProvider(provider model.Provider) error {
	_, err := db.conn.Exec("DELETE FROM replicas WHERE provider = ?", string(provider))
	return err
}

// DeleteReplica removes a specific replica by ID
func (db *DB) DeleteReplica(id int64) error {
	_, err := db.conn.Exec("DELETE FROM replicas WHERE id = ?", id)
	return err
}

// DeleteStaleReplicasByNativeID marks as deleted all replicas pointing to a stale native_id
// after a file has been transferred/moved. This prevents 404 errors when trying to download
// from replicas that reference a file that no longer exists.
func (db *DB) DeleteStaleReplicasByNativeID(provider model.Provider, oldNativeID string, excludeReplicaID int64) error {
	query := `
	UPDATE replicas
	SET status = 'deleted'
	WHERE provider = ? AND native_id = ? AND id != ? AND status = 'active'
	`
	_, err := db.conn.Exec(query, string(provider), oldNativeID, excludeReplicaID)
	return err
}

// UpdateSoftDeletedFileStatus marks files as softdeleted if ALL active replicas are in soft-deleted path
func (db *DB) UpdateSoftDeletedFileStatus() error {
	query := `
	UPDATE files
	SET status = 'softdeleted'
	WHERE status = 'active'
	AND id IN (
		SELECT f.id
		FROM files f
		WHERE f.status = 'active'
		-- File has at least one active replica
		AND EXISTS (
			SELECT 1
			FROM replicas r
			WHERE r.file_id = f.id 
			AND r.status = 'active'
		)
		-- ALL active replicas are in soft-deleted path (no replicas outside soft-deleted)
		AND NOT EXISTS (
			SELECT 1
			FROM replicas r
			WHERE r.file_id = f.id 
			AND r.status = 'active'
			AND r.path NOT LIKE '%sync-cloud-drives-aux/soft-deleted%'
			AND r.path NOT LIKE '%sync-cloud-drives-aux\soft-deleted%'
		)
	)
	`
	_, err := db.conn.Exec(query)
	return err
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
	connStr := fmt.Sprintf("file:%s?_pragma_key=%s", dbPath, url.QueryEscape(masterPassword))
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
	SELECT id, path, name, size, calculated_id, mod_time, status
	FROM files
	WHERE path = ?
	`
	row := db.conn.QueryRow(query, path)

	file := &model.File{}
	var modTime int64
	err := row.Scan(
		&file.ID, &file.Path, &file.Name, &file.Size,
		&file.CalculatedID, &modTime, &file.Status,
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
	SELECT id, path, name, size, calculated_id, mod_time, status
	FROM files
	WHERE id = ?
	`

	file := &model.File{}
	var modTime int64
	err := db.conn.QueryRow(query, id).Scan(
		&file.ID, &file.Path, &file.Name, &file.Size, &file.CalculatedID, &modTime, &file.Status,
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
	query := `
	UPDATE files 
	SET path = ?, name = ?, size = ?, calculated_id = ?, mod_time = ?, status = ?
	WHERE id = ?
	`
	_, err := db.conn.Exec(query,
		file.Path, file.Name, file.Size, file.CalculatedID, file.ModTime.Unix(), file.Status, file.ID)
	return err
}

// UpdateReplica updates a replica record
func (db *DB) UpdateReplica(replica *model.Replica) error {
	query := `
	UPDATE replicas SET
		file_id = ?, calculated_id = ?, path = ?, name = ?, size = ?,
		provider = ?, account_id = ?, native_id = ?, native_hash = ?,
		mod_time = ?, status = ?, fragmented = ?, owner = ?
	WHERE id = ?
	`
	_, err := db.conn.Exec(query,
		replica.FileID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
		string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
		replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.Owner, replica.ID)
	return err
}

// UpdateFileStatus updates the status of a file
func (db *DB) UpdateFileStatus(id string, status string) error {
	query := "UPDATE files SET status = ? WHERE id = ?"
	_, err := db.conn.Exec(query, status, id)
	return err
}

// UpdateFileModTime updates the modification time of a file
func (db *DB) UpdateFileModTime(id string, modTime time.Time) error {
	query := "UPDATE files SET mod_time = ? WHERE id = ?"
	_, err := db.conn.Exec(query, modTime.Unix(), id)
	return err
}

// UpdateReplicaFileID updates the file_id of a replica
func (db *DB) UpdateReplicaFileID(replicaID int64, fileID string) error {
	query := "UPDATE replicas SET file_id = ? WHERE id = ?"
	_, err := db.conn.Exec(query, fileID, replicaID)
	return err
}

// GetReplicasWithNullFileID returns all replicas without a file_id
func (db *DB) GetReplicasWithNullFileID() ([]*model.Replica, error) {
	query := `
	SELECT id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented, owner
	FROM replicas
	WHERE file_id IS NULL
	`
	rows, err := db.conn.Query(query)
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
		err := rows.Scan(&r.ID, &fileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
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

// LinkOrphanedReplicas links orphaned replicas to existing files based on calculated_id
func (db *DB) LinkOrphanedReplicas() error {
	// Update replicas that match an existing file by calculated_id
	// We use the first matching file if duplicates exist (though ideally calculated_id should be unique-ish)
	query := `
	UPDATE replicas
	SET file_id = (
		SELECT id FROM files 
		WHERE files.calculated_id = replicas.calculated_id 
		LIMIT 1
	)
	WHERE file_id IS NULL OR file_id = ''
	AND EXISTS (
		SELECT 1 FROM files 
		WHERE files.calculated_id = replicas.calculated_id
	)
	`
	_, err := db.conn.Exec(query)
	return err
}

// PromoteOrphanedReplicasToFiles creates new file records for replicas that don't match any existing file
func (db *DB) PromoteOrphanedReplicasToFiles() error {
	// Find replicas still without file_id
	query := `
	SELECT id, calculated_id, path, name, size, mod_time, status
	FROM replicas
	WHERE file_id IS NULL OR file_id = ''
	`
	rows, err := db.conn.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	// Need to collect data first to avoid locking issues if we modify inside loop with same connection
	type Orphan struct {
		ReplicaID    int64
		CalculatedID string
		Path         string
		Name         string
		Size         int64
		ModTime      int64
		Status       string
	}

	var orphans []Orphan
	for rows.Next() {
		var o Orphan
		if err := rows.Scan(&o.ReplicaID, &o.CalculatedID, &o.Path, &o.Name, &o.Size, &o.ModTime, &o.Status); err != nil {
			return err
		}
		orphans = append(orphans, o)
	}
	rows.Close()

	if len(orphans) == 0 {
		return nil
	}

	// Group orphans by calculated_id to merge replicas of the same file
	orphanGroups := make(map[string][]Orphan)
	for _, o := range orphans {
		orphanGroups[o.CalculatedID] = append(orphanGroups[o.CalculatedID], o)
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, group := range orphanGroups {
		// Use the first orphan's metadata for the new logical file
		first := group[0]
		newFileID := uuid.New().String()

		// Insert File
		_, err := tx.Exec(`
			INSERT OR IGNORE INTO files (id, path, name, size, calculated_id, mod_time, status)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, newFileID, first.Path, first.Name, first.Size, first.CalculatedID, first.ModTime, first.Status)
		if err != nil {
			return fmt.Errorf("failed to promote replica group %s: %w", first.CalculatedID, err)
		}

		// Update all replicas in the group
		for _, o := range group {
			_, err = tx.Exec(`
				UPDATE replicas SET file_id = ? WHERE id = ?
			`, newFileID, o.ReplicaID)
			if err != nil {
				return fmt.Errorf("failed to update replica %d: %w", o.ReplicaID, err)
			}
		}
	}

	return tx.Commit()
}

// UpdateLogicalFilesFromReplicas updates file metadata from the latest active replica
func (db *DB) UpdateLogicalFilesFromReplicas() error {
	// SQLite 3.33+ supported UPDATE FROM.
	// We want to pick the latest active replica for each file.
	// We prioritize replicas that indicate a change (path difference) if timestamps are equal.
	query := `
	WITH RankedReplicas AS (
		SELECT r.file_id, r.size, r.mod_time, r.calculated_id, r.name, r.path,
			ROW_NUMBER() OVER (
				PARTITION BY r.file_id 
				ORDER BY r.mod_time DESC, 
				         CASE WHEN r.path != f.path THEN 1 ELSE 0 END DESC
			) as rn
		FROM replicas r
		JOIN files f ON f.id = r.file_id
		WHERE r.status = 'active'
	)
	UPDATE files
	SET 
		size = rr.size,
		mod_time = rr.mod_time,
		calculated_id = rr.calculated_id,
		name = rr.name,
		path = rr.path
	FROM RankedReplicas rr
	WHERE files.id = rr.file_id
	AND rr.rn = 1
	AND rr.mod_time >= files.mod_time
	`
	_, err := db.conn.Exec(query)
	return err
}

// MarkDeletedReplicas marks replicas as deleted if they weren't seen since the given time
func (db *DB) MarkDeletedReplicas(startTime time.Time) error {
	query := `
	UPDATE replicas
	SET status = 'deleted'
	WHERE last_seen_at < ? AND status != 'deleted'
	`
	_, err := db.conn.Exec(query, startTime.Unix())
	return err
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
	err := db.conn.QueryRow(query, provider).Scan(&size)
	if err != nil {
		return 0, err
	}
	return size, nil
}
