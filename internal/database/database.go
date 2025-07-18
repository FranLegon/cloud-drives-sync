package database

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/model"
)

const (
	dbFile = "metadata.db"
)

// DB provides a high-level interface for all database operations, abstracting
// the underlying SQL implementation.
type DB interface {
	Close() error
	InitSchema() error
	UpsertFile(file *model.File) error
	UpsertFolder(folder *model.Folder) error
	DeleteFile(provider, fileID string) error
	DeleteFolder(provider, folderID string) error
	FindFolderByPath(provider, normalizedPath string) (*model.Folder, error)
	GetDuplicateHashes(provider string) (map[string]int, error)
	GetFilesByHash(provider, hash, hashAlgo string) ([]model.File, error)
	GetAllFilesByProvider(provider string) ([]model.File, error)
}

// sqliteDB is the concrete implementation of the DB interface for encrypted SQLite.
type sqliteDB struct {
	conn *sql.DB
}

// NewDB establishes a connection to the encrypted SQLite database file. It uses
// the master password as the decryption key for SQLCipher.
func NewDB(masterPassword string) (DB, error) {
	// We construct a DSN for go-sqlite3 with SQLCipher support.
	// The key is passed directly as a pragma. The user is 'owner' as required.
	// We also set a larger page size and enable foreign keys.
	// See: https://www.zetetic.net/sqlcipher/sqlcipher-api/#key
	// Note: QueryEscape is crucial for passwords containing special characters.
	dsn := fmt.Sprintf(
		"file:%s?_pragma_key=%s&_pragma_cipher_page_size=4096&_auth_user=owner",
		dbFile,
		url.QueryEscape(masterPassword),
	)

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite connection: %w", err)
	}

	// Ping verifies the connection and password are valid.
	if err = db.Ping(); err != nil {
		// If the DB file exists but ping fails, it's likely a wrong password.
		if _, statErr := os.Stat(dbFile); statErr == nil {
			return nil, fmt.Errorf("failed to connect to database (is the master password correct?): %w", err)
		}
		return nil, fmt.Errorf("failed to establish database connection: %w", err)
	}

	// Set connection pool settings for better performance.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(time.Hour)

	return &sqliteDB{conn: db}, nil
}

// InitSchema creates the necessary tables if they do not already exist.
func (db *sqliteDB) InitSchema() error {
	filesTableSQL := `
    CREATE TABLE IF NOT EXISTS files (
        FileID TEXT NOT NULL,
        Provider TEXT NOT NULL,
        OwnerEmail TEXT NOT NULL,
        FileHash TEXT NOT NULL,
        HashAlgorithm TEXT NOT NULL,
        FileName TEXT NOT NULL,
        FileSize INTEGER NOT NULL,
        ParentFolderID TEXT NOT NULL,
        CreatedOn DATETIME NOT NULL,
        LastModified DATETIME NOT NULL,
        LastSynced DATETIME NOT NULL,
        PRIMARY KEY (FileID, Provider)
    );`

	foldersTableSQL := `
    CREATE TABLE IF NOT EXISTS folders (
        FolderID TEXT NOT NULL,
        Provider TEXT NOT NULL,
        OwnerEmail TEXT NOT NULL,
        FolderName TEXT NOT NULL,
        ParentFolderID TEXT,
        Path TEXT NOT NULL,
        NormalizedPath TEXT NOT NULL UNIQUE,
        LastSynced DATETIME NOT NULL,
        PRIMARY KEY (FolderID, Provider)
    );
    CREATE INDEX IF NOT EXISTS idx_normalized_path ON folders(NormalizedPath);`

	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin schema transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(filesTableSQL); err != nil {
		return fmt.Errorf("failed to create 'files' table: %w", err)
	}
	if _, err := tx.Exec(foldersTableSQL); err != nil {
		return fmt.Errorf("failed to create 'folders' table: %w", err)
	}

	return tx.Commit()
}

// UpsertFile inserts a new file record or updates an existing one based on the primary key.
func (db *sqliteDB) UpsertFile(file *model.File) error {
	query := `
    INSERT INTO files (
        FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, 
        ParentFolderID, CreatedOn, LastModified, LastSynced
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(FileID, Provider) DO UPDATE SET
        OwnerEmail=excluded.OwnerEmail,
        FileHash=excluded.FileHash,
        HashAlgorithm=excluded.HashAlgorithm,
        FileName=excluded.FileName,
        FileSize=excluded.FileSize,
        ParentFolderID=excluded.ParentFolderID,
        CreatedOn=excluded.CreatedOn,
        LastModified=excluded.LastModified,
        LastSynced=excluded.LastSynced;`
	_, err := db.conn.Exec(query,
		file.FileID, file.Provider, file.OwnerEmail, file.FileHash, file.HashAlgorithm,
		file.FileName, file.FileSize, file.ParentFolderID, file.CreatedOn,
		file.LastModified, file.LastSynced,
	)
	return err
}

// UpsertFolder inserts a new folder record or updates an existing one.
func (db *sqliteDB) UpsertFolder(folder *model.Folder) error {
	query := `
    INSERT INTO folders (
        FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, 
        NormalizedPath, LastSynced
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(FolderID, Provider) DO UPDATE SET
        OwnerEmail=excluded.OwnerEmail,
        FolderName=excluded.FolderName,
        ParentFolderID=excluded.ParentFolderID,
        Path=excluded.Path,
        NormalizedPath=excluded.NormalizedPath,
        LastSynced=excluded.LastSynced;`
	_, err := db.conn.Exec(query,
		folder.FolderID, folder.Provider, folder.OwnerEmail, folder.FolderName,
		folder.ParentFolderID, folder.Path, folder.NormalizedPath, folder.LastSynced,
	)
	return err
}

// DeleteFile removes a file record from the database.
func (db *sqliteDB) DeleteFile(provider, fileID string) error {
	_, err := db.conn.Exec("DELETE FROM files WHERE Provider = ? AND FileID = ?", provider, fileID)
	return err
}

// DeleteFolder removes a folder record from the database.
func (db *sqliteDB) DeleteFolder(provider, folderID string) error {
	_, err := db.conn.Exec("DELETE FROM folders WHERE Provider = ? AND FolderID = ?", provider, folderID)
	return err
}

// FindFolderByPath retrieves a single folder by its provider and normalized path.
func (db *sqliteDB) FindFolderByPath(provider, normalizedPath string) (*model.Folder, error) {
	row := db.conn.QueryRow("SELECT FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath, LastSynced FROM folders WHERE Provider = ? AND NormalizedPath = ?", provider, normalizedPath)
	var f model.Folder
	err := row.Scan(&f.FolderID, &f.Provider, &f.OwnerEmail, &f.FolderName, &f.ParentFolderID, &f.Path, &f.NormalizedPath, &f.LastSynced)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Not found is not an error
		}
		return nil, err
	}
	return &f, nil
}

// GetDuplicateHashes finds all file hashes that appear more than once for a given provider.
func (db *sqliteDB) GetDuplicateHashes(provider string) (map[string]int, error) {
	query := "SELECT FileHash, COUNT(*) FROM files WHERE Provider = ? GROUP BY FileHash, HashAlgorithm HAVING COUNT(*) > 1"
	rows, err := db.conn.Query(query, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	duplicates := make(map[string]int)
	for rows.Next() {
		var hash string
		var count int
		if err := rows.Scan(&hash, &count); err != nil {
			return nil, err
		}
		duplicates[hash] = count
	}
	return duplicates, rows.Err()
}

// GetFilesByHash retrieves all file records matching a specific hash for a given provider.
func (db *sqliteDB) GetFilesByHash(provider, hash, hashAlgo string) ([]model.File, error) {
	query := "SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced FROM files WHERE Provider = ? AND FileHash = ? AND HashAlgorithm = ?"
	rows, err := db.conn.Query(query, provider, hash, hashAlgo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.File
	for rows.Next() {
		var f model.File
		err := rows.Scan(&f.FileID, &f.Provider, &f.OwnerEmail, &f.FileHash, &f.HashAlgorithm, &f.FileName, &f.FileSize, &f.ParentFolderID, &f.CreatedOn, &f.LastModified, &f.LastSynced)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// GetAllFilesByProvider retrieves all file records for a specific provider.
func (db *sqliteDB) GetAllFilesByProvider(provider string) ([]model.File, error) {
	query := "SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced FROM files WHERE Provider = ?"
	rows, err := db.conn.Query(query, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.File
	for rows.Next() {
		var f model.File
		err := rows.Scan(&f.FileID, &f.Provider, &f.OwnerEmail, &f.FileHash, &f.HashAlgorithm, &f.FileName, &f.FileSize, &f.ParentFolderID, &f.CreatedOn, &f.LastModified, &f.LastSynced)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// Close safely closes the database connection.
func (db *sqliteDB) Close() error {
	if db.conn != nil {
		return db.conn.Close()
	}
	return nil
}
