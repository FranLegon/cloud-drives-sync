package database

import (
	"cloud-drives-sync/internal/model"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "github.com/mattn/go-sqlite3" // SQLite driver with SQLCipher support
)

const (
	dbFile = "metadata.db"
)

// DB wraps the standard sql.DB connection pool.
type DB struct {
	*sql.DB
}

// getDBPath returns the absolute path to the database file.
func getDBPath() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exePath), dbFile), nil
}

// Connect opens and initializes the encrypted SQLite database using SQLCipher.
func Connect(password string) (*DB, error) {
	dbPath, err := getDBPath()
	if err != nil {
		return nil, err
	}

	// The DSN for SQLCipher requires a specific format to pass the key.
	// The password is URI-encoded to handle special characters.
	dsn := fmt.Sprintf("file:%s?_auth&_auth_user=owner&_auth_pass=%s&_auth_crypt=sqlcipher", dbPath, password)

	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	// Ping the database to verify the connection and password.
	if err = sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("failed to connect to encrypted database (check master password): %w", err)
	}

	db := &DB{sqlDB}
	if err = db.initSchema(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// initSchema creates the necessary tables if they don't already exist.
func (db *DB) initSchema() error {
	filesTable := `
    CREATE TABLE IF NOT EXISTS files (
        FileID TEXT,
        Provider TEXT,
        OwnerEmail TEXT,
        FileHash TEXT NOT NULL,
        HashAlgorithm TEXT NOT NULL,
        FileName TEXT,
        FileSize INTEGER,
        ParentFolderID TEXT,
        Path TEXT,
        NormalizedPath TEXT,
        CreatedOn DATETIME,
        LastModified DATETIME,
        LastSynced DATETIME,
        PRIMARY KEY (FileID, Provider)
    );`

	foldersTable := `
    CREATE TABLE IF NOT EXISTS folders (
        FolderID TEXT,
        Provider TEXT,
        OwnerEmail TEXT,
        FolderName TEXT,
        ParentFolderID TEXT,
        Path TEXT NOT NULL,
        NormalizedPath TEXT NOT NULL,
        LastSynced DATETIME,
        PRIMARY KEY (FolderID, Provider)
    );`

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() // Rollback on error, commit on success

	if _, err := tx.Exec(filesTable); err != nil {
		return err
	}
	if _, err := tx.Exec(foldersTable); err != nil {
		return err
	}
	return tx.Commit()
}

// UpsertFile inserts a new file record or updates an existing one based on the primary key.
func (db *DB) UpsertFile(file model.File) error {
	query := `
    INSERT INTO files (FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, Path, NormalizedPath, CreatedOn, LastModified, LastSynced)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(FileID, Provider) DO UPDATE SET
        OwnerEmail=excluded.OwnerEmail,
        FileHash=excluded.FileHash,
        HashAlgorithm=excluded.HashAlgorithm,
        FileName=excluded.FileName,
        FileSize=excluded.FileSize,
        ParentFolderID=excluded.ParentFolderID,
        Path=excluded.Path,
        NormalizedPath=excluded.NormalizedPath,
        LastModified=excluded.LastModified,
        LastSynced=excluded.LastSynced;`
	_, err := db.Exec(query, file.FileID, file.Provider, file.OwnerEmail, file.FileHash, file.HashAlgorithm, file.FileName, file.FileSize, file.ParentFolderID, file.Path, file.NormalizedPath, file.CreatedOn, file.LastModified, time.Now().UTC())
	return err
}

// UpsertFolder inserts a new folder record or updates an existing one.
func (db *DB) UpsertFolder(folder model.Folder) error {
	query := `
    INSERT INTO folders (FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath, LastSynced)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?)
    ON CONFLICT(FolderID, Provider) DO UPDATE SET
        OwnerEmail=excluded.OwnerEmail,
        FolderName=excluded.FolderName,
        ParentFolderID=excluded.ParentFolderID,
        Path=excluded.Path,
        NormalizedPath=excluded.NormalizedPath,
        LastSynced=excluded.LastSynced;`
	_, err := db.Exec(query, folder.FolderID, folder.Provider, folder.OwnerEmail, folder.FolderName, folder.ParentFolderID, folder.Path, folder.NormalizedPath, time.Now().UTC())
	return err
}

// GetFilesByProvider retrieves all file records for a given provider.
func (db *DB) GetFilesByProvider(provider string) ([]model.File, error) {
	rows, err := db.Query("SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, Path, NormalizedPath, CreatedOn, LastModified FROM files WHERE Provider = ?", provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.File
	for rows.Next() {
		var f model.File
		if err := rows.Scan(&f.FileID, &f.Provider, &f.OwnerEmail, &f.FileHash, &f.HashAlgorithm, &f.FileName, &f.FileSize, &f.ParentFolderID, &f.Path, &f.NormalizedPath, &f.CreatedOn, &f.LastModified); err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, nil
}

// GetFoldersByProvider retrieves all folder records for a given provider.
func (db *DB) GetFoldersByProvider(provider string) ([]model.Folder, error) {
	rows, err := db.Query("SELECT FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath FROM folders WHERE Provider = ?", provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []model.Folder
	for rows.Next() {
		var f model.Folder
		if err := rows.Scan(&f.FolderID, &f.Provider, &f.OwnerEmail, &f.FolderName, &f.ParentFolderID, &f.Path, &f.NormalizedPath); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, nil
}

// FindDuplicates queries for files with identical hashes within the same provider.
func (db *DB) FindDuplicates() (map[string][]model.File, error) {
	query := `
    SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, CreatedOn, Path
    FROM files
    WHERE (FileHash, HashAlgorithm, Provider) IN (
        SELECT FileHash, HashAlgorithm, Provider
        FROM files
        GROUP BY FileHash, HashAlgorithm, Provider
        HAVING COUNT(*) > 1
    )
    ORDER BY FileHash, Provider, CreatedOn;`

	rows, err := db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	duplicates := make(map[string][]model.File)
	for rows.Next() {
		var file model.File
		if err := rows.Scan(&file.FileID, &file.Provider, &file.OwnerEmail, &file.FileHash, &file.HashAlgorithm, &file.FileName, &file.FileSize, &file.CreatedOn, &file.Path); err != nil {
			return nil, err
		}
		// Group by a key of provider + hash
		hashKey := fmt.Sprintf("%s:%s", file.Provider, file.FileHash)
		duplicates[hashKey] = append(duplicates[hashKey], file)
	}
	return duplicates, nil
}

// DeleteFile removes a file record from the database.
func (db *DB) DeleteFile(fileID, provider string) error {
	_, err := db.Exec("DELETE FROM files WHERE FileID = ? AND Provider = ?", fileID, provider)
	return err
}
