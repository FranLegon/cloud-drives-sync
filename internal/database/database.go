package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
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
		FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_replicas_file_id ON replicas(file_id);
	CREATE INDEX IF NOT EXISTS idx_replicas_calculated_id ON replicas(calculated_id);
	CREATE INDEX IF NOT EXISTS idx_replicas_provider ON replicas(provider);
	CREATE INDEX IF NOT EXISTS idx_replicas_account_id ON replicas(account_id);
	CREATE INDEX IF NOT EXISTS idx_replicas_native_id ON replicas(native_id);
	CREATE INDEX IF NOT EXISTS idx_replicas_status ON replicas(status);

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

	_, err := db.conn.Exec(schema)
	return err
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
			file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`
		_, err := tx.Exec(replicaQuery,
			file.ID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
			string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
			replica.ModTime.Unix(), replica.Status, replica.Fragmented)
		if err != nil {
			return fmt.Errorf("failed to insert replica: %w", err)
		}

		// Note: Fragments should be inserted separately via InsertReplicaFragment
		// This is because fragments are associated with replicas, not files
	}

	return tx.Commit()
}

// BatchInsertFiles inserts multiple files in a single transaction
func (db *DB) BatchInsertFiles(files []*model.File) error {
	if len(files) == 0 {
		return nil
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	fileStmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO files (
			id, path, name, size, calculated_id, mod_time, status
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer fileStmt.Close()

	replicaStmt, err := tx.Prepare(`
		INSERT OR REPLACE INTO replicas (
			file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer replicaStmt.Close()

	for _, file := range files {
		_, err = fileStmt.Exec(
			file.ID, file.Path, file.Name, file.Size, file.CalculatedID, file.ModTime.Unix(), file.Status)
		if err != nil {
			return fmt.Errorf("failed to insert file: %w", err)
		}

		for _, replica := range file.Replicas {
			_, err = replicaStmt.Exec(
				file.ID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
				string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
				replica.ModTime.Unix(), replica.Status, replica.Fragmented)
			if err != nil {
				return fmt.Errorf("failed to insert replica: %w", err)
			}
		}
	}

	return tx.Commit()
}

// InsertReplica inserts a replica record into the database
func (db *DB) InsertReplica(replica *model.Replica) error {
	query := `
	INSERT INTO replicas (
		file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	res, err := db.conn.Exec(query,
		replica.FileID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
		string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
		replica.ModTime.Unix(), replica.Status, replica.Fragmented)
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
		id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := db.conn.Exec(query,
		replica.ID, replica.FileID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
		string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
		replica.ModTime.Unix(), replica.Status, replica.Fragmented)
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

// GetAllFilesAcrossProviders returns all files (alias for GetAllFiles for backwards compatibility)
func (db *DB) GetAllFilesAcrossProviders() ([]*model.File, error) {
	return db.GetAllFiles()
}

// GetReplicas returns all replicas for a file
func (db *DB) GetReplicas(fileID string) ([]*model.Replica, error) {
	query := `
	SELECT id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented
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
		err := rows.Scan(&r.ID, &r.FileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
			&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented)
		if err != nil {
			return nil, err
		}
		r.Provider = model.Provider(providerStr)
		r.ModTime = time.Unix(modTime, 0)
		replicas = append(replicas, r)
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
	SELECT id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented
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
		err := rows.Scan(&r.ID, &r.FileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
			&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented)
		if err != nil {
			return nil, err
		}
		r.Provider = model.Provider(providerStr)
		r.ModTime = time.Unix(modTime, 0)
		replicas = append(replicas, r)
	}
	return replicas, nil
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
		mod_time = ?, status = ?, fragmented = ?
	WHERE id = ?
	`
	_, err := db.conn.Exec(query,
		replica.FileID, replica.CalculatedID, replica.Path, replica.Name, replica.Size,
		string(replica.Provider), replica.AccountID, replica.NativeID, replica.NativeHash,
		replica.ModTime.Unix(), replica.Status, replica.Fragmented, replica.ID)
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
	SELECT id, file_id, calculated_id, path, name, size, provider, account_id, native_id, native_hash, mod_time, status, fragmented
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
		err := rows.Scan(&r.ID, &fileID, &r.CalculatedID, &r.Path, &r.Name, &r.Size,
			&providerStr, &r.AccountID, &r.NativeID, &r.NativeHash, &modTime, &r.Status, &r.Fragmented)
		if err != nil {
			return nil, err
		}
		r.Provider = model.Provider(providerStr)
		r.ModTime = time.Unix(modTime, 0)
		if fileID.Valid {
			r.FileID = fileID.String
		}
		replicas = append(replicas, r)
	}
	return replicas, nil
}
