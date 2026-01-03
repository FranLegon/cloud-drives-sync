package database

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/model"
	_ "github.com/mattn/go-sqlite3"
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
	
	// For SQLCipher, we use the password with PRAGMA key
	// Note: go-sqlite3 can be built with SQLCipher support using build tags
	connStr := fmt.Sprintf("file:%s?_auth&_auth_user=%s&_auth_pass=%s", dbPath, DBUser, masterPassword)
	
	conn, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, err
	}

	// Set SQLCipher key using PRAGMA
	if _, err := conn.Exec(fmt.Sprintf("PRAGMA key = '%s';", masterPassword)); err != nil {
		conn.Close()
		return nil, err
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
		name TEXT NOT NULL,
		path TEXT NOT NULL,
		size INTEGER NOT NULL,
		hash TEXT NOT NULL,
		hash_algorithm TEXT NOT NULL,
		provider TEXT NOT NULL,
		user_email TEXT,
		user_phone TEXT,
		created_time DATETIME NOT NULL,
		modified_time DATETIME NOT NULL,
		owner_email TEXT,
		parent_folder_id TEXT
	);

	CREATE INDEX IF NOT EXISTS idx_files_hash ON files(hash);
	CREATE INDEX IF NOT EXISTS idx_files_provider ON files(provider);
	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);

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
	query := `
	INSERT OR REPLACE INTO files (
		id, name, path, size, hash, hash_algorithm, provider, 
		user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := db.conn.Exec(query,
		file.ID, file.Name, file.Path, file.Size, file.Hash, file.HashAlgorithm,
		string(file.Provider), file.UserEmail, file.UserPhone,
		file.CreatedTime, file.ModifiedTime, file.OwnerEmail, file.ParentFolderID,
	)
	return err
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

// GetFilesByHash returns all files with a specific hash
func (db *DB) GetFilesByHash(hash string, provider model.Provider) ([]*model.File, error) {
	query := `
	SELECT id, name, path, size, hash, hash_algorithm, provider,
		   user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id
	FROM files
	WHERE hash = ? AND provider = ?
	ORDER BY created_time ASC
	`

	rows, err := db.conn.Query(query, hash, string(provider))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var providerStr string
		err := rows.Scan(
			&file.ID, &file.Name, &file.Path, &file.Size, &file.Hash, &file.HashAlgorithm,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
		)
		if err != nil {
			return nil, err
		}
		file.Provider = model.Provider(providerStr)
		files = append(files, file)
	}

	return files, rows.Err()
}

// GetAllFiles returns all files for a provider
func (db *DB) GetAllFiles(provider model.Provider) ([]*model.File, error) {
	query := `
	SELECT id, name, path, size, hash, hash_algorithm, provider,
		   user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id
	FROM files
	WHERE provider = ?
	ORDER BY path ASC
	`

	rows, err := db.conn.Query(query, string(provider))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var providerStr string
		err := rows.Scan(
			&file.ID, &file.Name, &file.Path, &file.Size, &file.Hash, &file.HashAlgorithm,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
		)
		if err != nil {
			return nil, err
		}
		file.Provider = model.Provider(providerStr)
		files = append(files, file)
	}

	return files, rows.Err()
}

// GetFilesByUserEmail returns all files for a specific user email
func (db *DB) GetFilesByUserEmail(provider model.Provider, email string) ([]*model.File, error) {
	query := `
	SELECT id, name, path, size, hash, hash_algorithm, provider,
		   user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id
	FROM files
	WHERE provider = ? AND user_email = ?
	ORDER BY size DESC
	`

	rows, err := db.conn.Query(query, string(provider), email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var providerStr string
		err := rows.Scan(
			&file.ID, &file.Name, &file.Path, &file.Size, &file.Hash, &file.HashAlgorithm,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
		)
		if err != nil {
			return nil, err
		}
		file.Provider = model.Provider(providerStr)
		files = append(files, file)
	}

	return files, rows.Err()
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

// GetDuplicateHashes returns hashes that appear more than once for a provider
func (db *DB) GetDuplicateHashes(provider model.Provider) ([]string, error) {
	query := `
	SELECT hash
	FROM files
	WHERE provider = ?
	GROUP BY hash
	HAVING COUNT(*) > 1
	`

	rows, err := db.conn.Query(query, string(provider))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			return nil, err
		}
		hashes = append(hashes, hash)
	}

	return hashes, rows.Err()
}

// ClearProvider removes all files and folders for a provider
func (db *DB) ClearProvider(provider model.Provider) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM files WHERE provider = ?", string(provider)); err != nil {
		return err
	}

	if _, err := tx.Exec("DELETE FROM folders WHERE provider = ?", string(provider)); err != nil {
		return err
	}

	return tx.Commit()
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
	
	// Create database file
	connStr := fmt.Sprintf("file:%s?_auth&_auth_user=%s&_auth_pass=%s", dbPath, DBUser, masterPassword)
	conn, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Set SQLCipher key
	if _, err := conn.Exec(fmt.Sprintf("PRAGMA key = '%s';", masterPassword)); err != nil {
		return err
	}

	// Create a test table to initialize the database
	if _, err := conn.Exec("CREATE TABLE IF NOT EXISTS _init (id INTEGER PRIMARY KEY)"); err != nil {
		return err
	}

	return nil
}

// GetFileByID retrieves a file by its ID
func (db *DB) GetFileByID(id string) (*model.File, error) {
	query := `
	SELECT id, name, path, size, hash, hash_algorithm, provider,
		   user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id
	FROM files
	WHERE id = ?
	`

	file := &model.File{}
	var providerStr string
	err := db.conn.QueryRow(query, id).Scan(
		&file.ID, &file.Name, &file.Path, &file.Size, &file.Hash, &file.HashAlgorithm,
		&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
		&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
	)
	if err != nil {
		return nil, err
	}
	file.Provider = model.Provider(providerStr)
	return file, nil
}

// GetAllFilesAcrossProviders returns all files across all providers
func (db *DB) GetAllFilesAcrossProviders() ([]*model.File, error) {
	query := `
	SELECT id, name, path, size, hash, hash_algorithm, provider,
		   user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id
	FROM files
	ORDER BY provider, path ASC
	`

	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var providerStr string
		err := rows.Scan(
			&file.ID, &file.Name, &file.Path, &file.Size, &file.Hash, &file.HashAlgorithm,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
		)
		if err != nil {
			return nil, err
		}
		file.Provider = model.Provider(providerStr)
		files = append(files, file)
	}

	return files, rows.Err()
}

// UpdateFileModifiedTime updates the modified time of a file
func (db *DB) UpdateFileModifiedTime(id string, modifiedTime time.Time) error {
	query := "UPDATE files SET modified_time = ? WHERE id = ?"
	_, err := db.conn.Exec(query, modifiedTime, id)
	return err
}
