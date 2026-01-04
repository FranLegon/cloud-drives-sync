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
		googledrive_hash TEXT,
		googledrive_id TEXT,
		onedrive_hash TEXT,
		onedrive_id TEXT,
		telegram_unique_id TEXT,
		calculated_sha256_hash TEXT,
		calculated_id TEXT,
		provider TEXT NOT NULL,
		user_email TEXT,
		user_phone TEXT,
		created_time DATETIME NOT NULL,
		modified_time DATETIME NOT NULL,
		owner_email TEXT,
		parent_folder_id TEXT,
		split BOOLEAN DEFAULT 0,
		total_parts INTEGER DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_files_calculated_id ON files(calculated_id);
	CREATE INDEX IF NOT EXISTS idx_files_provider ON files(provider);
	CREATE INDEX IF NOT EXISTS idx_files_path ON files(path);

	CREATE TABLE IF NOT EXISTS files_fragments (
		id TEXT PRIMARY KEY,
		file_id TEXT NOT NULL,
		name TEXT NOT NULL,
		size INTEGER NOT NULL,
		part INTEGER NOT NULL,
		telegram_unique_id TEXT,
		FOREIGN KEY(file_id) REFERENCES files(id) ON DELETE CASCADE
	);

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
		id, name, path, size, 
		googledrive_hash, googledrive_id, onedrive_hash, onedrive_id, telegram_unique_id,
		calculated_sha256_hash, calculated_id,
		provider, user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id,
		split, total_parts
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	_, err := db.conn.Exec(query,
		file.ID, file.Name, file.Path, file.Size,
		file.GoogleDriveHash, file.GoogleDriveID, file.OneDriveHash, file.OneDriveID, file.TelegramUniqueID,
		file.CalculatedSHA256Hash, file.CalculatedID,
		string(file.Provider), file.UserEmail, file.UserPhone,
		file.CreatedTime, file.ModifiedTime, file.OwnerEmail, file.ParentFolderID,
		file.Split, file.TotalParts,
	)
	return err
}

// InsertFileFragment inserts a file fragment record into the database
func (db *DB) InsertFileFragment(fragment *model.FileFragment) error {
	query := `
	INSERT OR REPLACE INTO files_fragments (
		id, file_id, name, size, part, telegram_unique_id
	) VALUES (?, ?, ?, ?, ?, ?)
	`

	_, err := db.conn.Exec(query,
		fragment.ID, fragment.FileID, fragment.Name, fragment.Size, fragment.Part, fragment.TelegramUniqueID,
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

// GetFilesByCalculatedID returns all files with a specific calculated ID
func (db *DB) GetFilesByCalculatedID(calculatedID string, provider model.Provider) ([]*model.File, error) {
	query := `
	SELECT id, name, path, size, 
		   googledrive_hash, googledrive_id, onedrive_hash, onedrive_id, telegram_unique_id,
		   calculated_sha256_hash, calculated_id,
		   provider, user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id,
		   split, total_parts
	FROM files
	WHERE calculated_id = ? AND provider = ?
	ORDER BY created_time ASC
	`

	rows, err := db.conn.Query(query, calculatedID, string(provider))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []*model.File
	for rows.Next() {
		file := &model.File{}
		var providerStr string
		err := rows.Scan(
			&file.ID, &file.Name, &file.Path, &file.Size,
			&file.GoogleDriveHash, &file.GoogleDriveID, &file.OneDriveHash, &file.OneDriveID, &file.TelegramUniqueID,
			&file.CalculatedSHA256Hash, &file.CalculatedID,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
			&file.Split, &file.TotalParts,
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
	SELECT id, name, path, size, 
		   googledrive_hash, googledrive_id, onedrive_hash, onedrive_id, telegram_unique_id,
		   calculated_sha256_hash, calculated_id,
		   provider, user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id,
		   split, total_parts
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
			&file.ID, &file.Name, &file.Path, &file.Size,
			&file.GoogleDriveHash, &file.GoogleDriveID, &file.OneDriveHash, &file.OneDriveID, &file.TelegramUniqueID,
			&file.CalculatedSHA256Hash, &file.CalculatedID,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
			&file.Split, &file.TotalParts,
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
	SELECT id, name, path, size, 
		   googledrive_hash, googledrive_id, onedrive_hash, onedrive_id, telegram_unique_id,
		   calculated_sha256_hash, calculated_id,
		   provider, user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id,
		   split, total_parts
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
			&file.ID, &file.Name, &file.Path, &file.Size,
			&file.GoogleDriveHash, &file.GoogleDriveID, &file.OneDriveHash, &file.OneDriveID, &file.TelegramUniqueID,
			&file.CalculatedSHA256Hash, &file.CalculatedID,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
			&file.Split, &file.TotalParts,
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

// GetDuplicateCalculatedIDs returns calculated IDs that appear more than once for a provider
func (db *DB) GetDuplicateCalculatedIDs(provider model.Provider) ([]string, error) {
	query := `
	SELECT calculated_id
	FROM files
	WHERE provider = ?
	GROUP BY calculated_id
	HAVING COUNT(*) > 1
	`

	rows, err := db.conn.Query(query, string(provider))
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

	return ids, rows.Err()
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
	SELECT id, name, path, size, 
		   googledrive_hash, googledrive_id, onedrive_hash, onedrive_id, telegram_unique_id,
		   calculated_sha256_hash, calculated_id,
		   provider, user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id,
		   split, total_parts
	FROM files
	WHERE id = ?
	`

	file := &model.File{}
	var providerStr string
	err := db.conn.QueryRow(query, id).Scan(
		&file.ID, &file.Name, &file.Path, &file.Size,
		&file.GoogleDriveHash, &file.GoogleDriveID, &file.OneDriveHash, &file.OneDriveID, &file.TelegramUniqueID,
		&file.CalculatedSHA256Hash, &file.CalculatedID,
		&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
		&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
		&file.Split, &file.TotalParts,
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
	SELECT id, name, path, size, 
		   googledrive_hash, googledrive_id, onedrive_hash, onedrive_id, telegram_unique_id,
		   calculated_sha256_hash, calculated_id,
		   provider, user_email, user_phone, created_time, modified_time, owner_email, parent_folder_id,
		   split, total_parts
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
			&file.ID, &file.Name, &file.Path, &file.Size,
			&file.GoogleDriveHash, &file.GoogleDriveID, &file.OneDriveHash, &file.OneDriveID, &file.TelegramUniqueID,
			&file.CalculatedSHA256Hash, &file.CalculatedID,
			&providerStr, &file.UserEmail, &file.UserPhone, &file.CreatedTime,
			&file.ModifiedTime, &file.OwnerEmail, &file.ParentFolderID,
			&file.Split, &file.TotalParts,
		)
		if err != nil {
			return nil, err
		}
		file.Provider = model.Provider(providerStr)
		files = append(files, file)
	}

	return files, rows.Err()
}

// GetFileFragments returns all fragments for a given file ID
func (db *DB) GetFileFragments(fileID string) ([]*model.FileFragment, error) {
	query := `
	SELECT id, file_id, name, size, part, telegram_unique_id
	FROM files_fragments
	WHERE file_id = ?
	ORDER BY part ASC
	`

	rows, err := db.conn.Query(query, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fragments []*model.FileFragment
	for rows.Next() {
		fragment := &model.FileFragment{}
		err := rows.Scan(
			&fragment.ID, &fragment.FileID, &fragment.Name, &fragment.Size,
			&fragment.Part, &fragment.TelegramUniqueID,
		)
		if err != nil {
			return nil, err
		}
		fragments = append(fragments, fragment)
	}

	return fragments, rows.Err()
}

// UpdateFileModifiedTime updates the modified time of a file
func (db *DB) UpdateFileModifiedTime(id string, modifiedTime time.Time) error {
	query := "UPDATE files SET modified_time = ? WHERE id = ?"
	_, err := db.conn.Exec(query, modifiedTime, id)
	return err
}
