package database

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"cloud-drives-sync/internal/model"

	_ "github.com/mattn/go-sqlite3"
)

const (
	DBFileName = "bin/metadata.db"
	DBUser     = "owner"
)

// Database defines the interface for database operations
type Database interface {
	Close() error

	// File operations
	UpsertFile(file *model.File) error
	GetFile(fileID string, provider model.Provider) (*model.File, error)
	GetFilesByHash(hash string, hashAlgorithm string, provider model.Provider) ([]model.File, error)
	GetAllFiles(provider model.Provider, ownerEmail string) ([]model.File, error)
	GetAllFilesByProvider(provider model.Provider) ([]model.File, error)
	DeleteFile(fileID string, provider model.Provider) error

	// Folder operations
	UpsertFolder(folder *model.Folder) error
	GetFolder(folderID string, provider model.Provider) (*model.Folder, error)
	GetFolderByPath(normalizedPath string, provider model.Provider) (*model.Folder, error)
	GetFoldersByPath(normalizedPath string, provider model.Provider) ([]model.Folder, error)
	GetAllFolders(provider model.Provider, ownerEmail string) ([]model.Folder, error)
	DeleteFolder(folderID string, provider model.Provider) error

	// Duplicate detection
	FindDuplicates(provider model.Provider) (map[string][]model.File, error)

	// Computed hash operations
	UpsertComputedHash(fileID string, provider model.Provider, googleMD5, microsoftB64, sha256 string) error
	GetComputedHash(fileID string, provider model.Provider) (*model.ComputedHash, error)
}

// SQLiteDB implements the Database interface using SQLite with SQLCipher
type SQLiteDB struct {
	db *sql.DB
}

// NewDatabase creates and initializes a new encrypted SQLite database
func NewDatabase(password string) (Database, error) {
	// Connection string with SQLCipher encryption
	// Note: SQLCipher uses PRAGMA key for encryption
	connStr := fmt.Sprintf("file:%s?_pragma_key=%s&_pragma_cipher_page_size=4096", DBFileName, password)

	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test the connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	sqlDB := &SQLiteDB{db: db}

	// Create tables if they don't exist
	if err := sqlDB.createTables(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return sqlDB, nil
}

// createTables creates the necessary database tables
func (d *SQLiteDB) createTables() error {
	filesTable := `
	CREATE TABLE IF NOT EXISTS files (
		FileID TEXT NOT NULL,
		Provider TEXT NOT NULL,
		OwnerEmail TEXT NOT NULL,
		FileHash TEXT NOT NULL,
		HashAlgorithm TEXT NOT NULL,
		FileName TEXT,
		FileSize INTEGER,
		ParentFolderID TEXT,
		CreatedOn DATETIME,
		LastModified DATETIME,
		LastSynced DATETIME,
		PRIMARY KEY (FileID, Provider)
	);`

	foldersTable := `
	CREATE TABLE IF NOT EXISTS folders (
		FolderID TEXT NOT NULL,
		Provider TEXT NOT NULL,
		OwnerEmail TEXT NOT NULL,
		FolderName TEXT,
		ParentFolderID TEXT,
		Path TEXT NOT NULL,
		NormalizedPath TEXT NOT NULL,
		LastSynced DATETIME,
		PRIMARY KEY (FolderID, Provider)
	);`

	computedHashesTable := `
	CREATE TABLE IF NOT EXISTS computed_hashes (
		FileID TEXT NOT NULL,
		Provider TEXT NOT NULL,
		GoogleMD5Hash TEXT,
		MicrosoftB64Hash TEXT,
		MySha256Hash TEXT NOT NULL,
		ComputedAt DATETIME DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (FileID, Provider)
	);`

	if _, err := d.db.Exec(filesTable); err != nil {
		return fmt.Errorf("failed to create files table: %w", err)
	}

	if _, err := d.db.Exec(foldersTable); err != nil {
		return fmt.Errorf("failed to create folders table: %w", err)
	}

	if _, err := d.db.Exec(computedHashesTable); err != nil {
		return fmt.Errorf("failed to create computed_hashes table: %w", err)
	}

	// Create indexes for common queries
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_files_hash ON files(FileHash, HashAlgorithm, Provider);",
		"CREATE INDEX IF NOT EXISTS idx_files_owner ON files(OwnerEmail, Provider);",
		"CREATE INDEX IF NOT EXISTS idx_folders_path ON folders(NormalizedPath, Provider);",
		"CREATE INDEX IF NOT EXISTS idx_folders_owner ON folders(OwnerEmail, Provider);",
		"CREATE INDEX IF NOT EXISTS idx_computed_hashes_sha256 ON computed_hashes(MySha256Hash);",
	}

	for _, idx := range indexes {
		if _, err := d.db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}

	return nil
}

// Close closes the database connection
func (d *SQLiteDB) Close() error {
	return d.db.Close()
}

// UpsertFile inserts or updates a file record
func (d *SQLiteDB) UpsertFile(file *model.File) error {
	query := `
	INSERT INTO files (FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(FileID, Provider) DO UPDATE SET
		OwnerEmail = excluded.OwnerEmail,
		FileHash = excluded.FileHash,
		HashAlgorithm = excluded.HashAlgorithm,
		FileName = excluded.FileName,
		FileSize = excluded.FileSize,
		ParentFolderID = excluded.ParentFolderID,
		CreatedOn = excluded.CreatedOn,
		LastModified = excluded.LastModified,
		LastSynced = excluded.LastSynced;
	`

	_, err := d.db.Exec(query,
		file.FileID,
		file.Provider,
		file.OwnerEmail,
		file.FileHash,
		file.HashAlgorithm,
		file.FileName,
		file.FileSize,
		file.ParentFolderID,
		file.CreatedOn,
		file.LastModified,
		file.LastSynced,
	)

	return err
}

// GetFile retrieves a file by ID and provider
func (d *SQLiteDB) GetFile(fileID string, provider model.Provider) (*model.File, error) {
	query := `SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced FROM files WHERE FileID = ? AND Provider = ?`

	var file model.File
	err := d.db.QueryRow(query, fileID, provider).Scan(
		&file.FileID,
		&file.Provider,
		&file.OwnerEmail,
		&file.FileHash,
		&file.HashAlgorithm,
		&file.FileName,
		&file.FileSize,
		&file.ParentFolderID,
		&file.CreatedOn,
		&file.LastModified,
		&file.LastSynced,
	)

	if err == sql.ErrNoRows {
		return nil, errors.New("file not found")
	}
	if err != nil {
		return nil, err
	}

	return &file, nil
}

// GetFilesByHash retrieves all files with a specific hash within a provider
func (d *SQLiteDB) GetFilesByHash(hash string, hashAlgorithm string, provider model.Provider) ([]model.File, error) {
	query := `SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced FROM files WHERE FileHash = ? AND HashAlgorithm = ? AND Provider = ?`

	rows, err := d.db.Query(query, hash, hashAlgorithm, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.File
	for rows.Next() {
		var file model.File
		err := rows.Scan(
			&file.FileID,
			&file.Provider,
			&file.OwnerEmail,
			&file.FileHash,
			&file.HashAlgorithm,
			&file.FileName,
			&file.FileSize,
			&file.ParentFolderID,
			&file.CreatedOn,
			&file.LastModified,
			&file.LastSynced,
		)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}

	return files, rows.Err()
}

// GetAllFiles retrieves all files for a provider and owner
func (d *SQLiteDB) GetAllFiles(provider model.Provider, ownerEmail string) ([]model.File, error) {
	query := `SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced FROM files WHERE Provider = ? AND OwnerEmail = ?`

	rows, err := d.db.Query(query, provider, ownerEmail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.File
	for rows.Next() {
		var file model.File
		err := rows.Scan(
			&file.FileID,
			&file.Provider,
			&file.OwnerEmail,
			&file.FileHash,
			&file.HashAlgorithm,
			&file.FileName,
			&file.FileSize,
			&file.ParentFolderID,
			&file.CreatedOn,
			&file.LastModified,
			&file.LastSynced,
		)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}

	return files, rows.Err()
}

// GetAllFilesByProvider retrieves all files for a provider regardless of owner
func (d *SQLiteDB) GetAllFilesByProvider(provider model.Provider) ([]model.File, error) {
	query := `SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced FROM files WHERE Provider = ?`

	rows, err := d.db.Query(query, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var files []model.File
	for rows.Next() {
		var file model.File
		err := rows.Scan(
			&file.FileID,
			&file.Provider,
			&file.OwnerEmail,
			&file.FileHash,
			&file.HashAlgorithm,
			&file.FileName,
			&file.FileSize,
			&file.ParentFolderID,
			&file.CreatedOn,
			&file.LastModified,
			&file.LastSynced,
		)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}

	return files, rows.Err()
}

// DeleteFile removes a file from the database
func (d *SQLiteDB) DeleteFile(fileID string, provider model.Provider) error {
	query := `DELETE FROM files WHERE FileID = ? AND Provider = ?`
	_, err := d.db.Exec(query, fileID, provider)
	return err
}

// UpsertFolder inserts or updates a folder record
func (d *SQLiteDB) UpsertFolder(folder *model.Folder) error {
	query := `
	INSERT INTO folders (FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath, LastSynced)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(FolderID, Provider) DO UPDATE SET
		OwnerEmail = excluded.OwnerEmail,
		FolderName = excluded.FolderName,
		ParentFolderID = excluded.ParentFolderID,
		Path = excluded.Path,
		NormalizedPath = excluded.NormalizedPath,
		LastSynced = excluded.LastSynced;
	`

	_, err := d.db.Exec(query,
		folder.FolderID,
		folder.Provider,
		folder.OwnerEmail,
		folder.FolderName,
		folder.ParentFolderID,
		folder.Path,
		folder.NormalizedPath,
		folder.LastSynced,
	)

	return err
}

// GetFolder retrieves a folder by ID and provider
func (d *SQLiteDB) GetFolder(folderID string, provider model.Provider) (*model.Folder, error) {
	query := `SELECT FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath, LastSynced FROM folders WHERE FolderID = ? AND Provider = ?`

	var folder model.Folder
	err := d.db.QueryRow(query, folderID, provider).Scan(
		&folder.FolderID,
		&folder.Provider,
		&folder.OwnerEmail,
		&folder.FolderName,
		&folder.ParentFolderID,
		&folder.Path,
		&folder.NormalizedPath,
		&folder.LastSynced,
	)

	if err == sql.ErrNoRows {
		return nil, errors.New("folder not found")
	}
	if err != nil {
		return nil, err
	}

	return &folder, nil
}

// GetFolderByPath retrieves a folder by normalized path and provider
func (d *SQLiteDB) GetFolderByPath(normalizedPath string, provider model.Provider) (*model.Folder, error) {
	query := `SELECT FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath, LastSynced FROM folders WHERE NormalizedPath = ? AND Provider = ?`

	var folder model.Folder
	err := d.db.QueryRow(query, normalizedPath, provider).Scan(
		&folder.FolderID,
		&folder.Provider,
		&folder.OwnerEmail,
		&folder.FolderName,
		&folder.ParentFolderID,
		&folder.Path,
		&folder.NormalizedPath,
		&folder.LastSynced,
	)

	if err == sql.ErrNoRows {
		return nil, errors.New("folder not found")
	}
	if err != nil {
		return nil, err
	}

	return &folder, nil
}

// GetFoldersByPath retrieves all folders with a specific normalized path and provider
func (d *SQLiteDB) GetFoldersByPath(normalizedPath string, provider model.Provider) ([]model.Folder, error) {
	query := `SELECT FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath, LastSynced FROM folders WHERE NormalizedPath = ? AND Provider = ?`

	rows, err := d.db.Query(query, normalizedPath, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []model.Folder
	for rows.Next() {
		var folder model.Folder
		err := rows.Scan(
			&folder.FolderID,
			&folder.Provider,
			&folder.OwnerEmail,
			&folder.FolderName,
			&folder.ParentFolderID,
			&folder.Path,
			&folder.NormalizedPath,
			&folder.LastSynced,
		)
		if err != nil {
			return nil, err
		}
		folders = append(folders, folder)
	}

	return folders, rows.Err()
}

// GetAllFolders retrieves all folders for a provider and owner
func (d *SQLiteDB) GetAllFolders(provider model.Provider, ownerEmail string) ([]model.Folder, error) {
	query := `SELECT FolderID, Provider, OwnerEmail, FolderName, ParentFolderID, Path, NormalizedPath, LastSynced FROM folders WHERE Provider = ? AND OwnerEmail = ?`

	rows, err := d.db.Query(query, provider, ownerEmail)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var folders []model.Folder
	for rows.Next() {
		var folder model.Folder
		err := rows.Scan(
			&folder.FolderID,
			&folder.Provider,
			&folder.OwnerEmail,
			&folder.FolderName,
			&folder.ParentFolderID,
			&folder.Path,
			&folder.NormalizedPath,
			&folder.LastSynced,
		)
		if err != nil {
			return nil, err
		}
		folders = append(folders, folder)
	}

	return folders, rows.Err()
}

// DeleteFolder removes a folder from the database
func (d *SQLiteDB) DeleteFolder(folderID string, provider model.Provider) error {
	query := `DELETE FROM folders WHERE FolderID = ? AND Provider = ?`
	_, err := d.db.Exec(query, folderID, provider)
	return err
}

// FindDuplicates finds all duplicate files within a provider
func (d *SQLiteDB) FindDuplicates(provider model.Provider) (map[string][]model.File, error) {
	query := `
	SELECT FileID, Provider, OwnerEmail, FileHash, HashAlgorithm, FileName, FileSize, ParentFolderID, CreatedOn, LastModified, LastSynced
	FROM files
	WHERE Provider = ? AND (FileHash, HashAlgorithm) IN (
		SELECT FileHash, HashAlgorithm
		FROM files
		WHERE Provider = ?
		GROUP BY FileHash, HashAlgorithm
		HAVING COUNT(*) > 1
	)
	ORDER BY FileHash, HashAlgorithm, CreatedOn;
	`

	rows, err := d.db.Query(query, provider, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	duplicates := make(map[string][]model.File)
	for rows.Next() {
		var file model.File
		err := rows.Scan(
			&file.FileID,
			&file.Provider,
			&file.OwnerEmail,
			&file.FileHash,
			&file.HashAlgorithm,
			&file.FileName,
			&file.FileSize,
			&file.ParentFolderID,
			&file.CreatedOn,
			&file.LastModified,
			&file.LastSynced,
		)
		if err != nil {
			return nil, err
		}

		key := file.FileHash + ":" + file.HashAlgorithm
		duplicates[key] = append(duplicates[key], file)
	}

	return duplicates, rows.Err()
}

// UpsertComputedHash inserts or updates computed hash values for a file
func (d *SQLiteDB) UpsertComputedHash(fileID string, provider model.Provider, googleMD5, microsoftB64, sha256 string) error {
	query := `
	INSERT INTO computed_hashes (FileID, Provider, GoogleMD5Hash, MicrosoftB64Hash, MySha256Hash, ComputedAt)
	VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(FileID, Provider) DO UPDATE SET
		GoogleMD5Hash = excluded.GoogleMD5Hash,
		MicrosoftB64Hash = excluded.MicrosoftB64Hash,
		MySha256Hash = excluded.MySha256Hash,
		ComputedAt = CURRENT_TIMESTAMP;
	`

	_, err := d.db.Exec(query, fileID, provider, googleMD5, microsoftB64, sha256)
	return err
}

// GetComputedHash retrieves computed hash values for a file
func (d *SQLiteDB) GetComputedHash(fileID string, provider model.Provider) (*model.ComputedHash, error) {
	query := `SELECT FileID, Provider, GoogleMD5Hash, MicrosoftB64Hash, MySha256Hash, ComputedAt FROM computed_hashes WHERE FileID = ? AND Provider = ?`

	var hash model.ComputedHash
	err := d.db.QueryRow(query, fileID, provider).Scan(
		&hash.FileID,
		&hash.Provider,
		&hash.GoogleMD5Hash,
		&hash.MicrosoftB64Hash,
		&hash.MySha256Hash,
		&hash.ComputedAt,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return &hash, nil
}

// SetupDatabase initializes the database with proper user/password (for SQLCipher)
func SetupDatabase(password string) error {
	db, err := NewDatabase(password)
	if err != nil {
		return err
	}
	defer db.Close()

	// Test that we can insert and read
	testFile := &model.File{
		FileID:        "test",
		Provider:      model.ProviderGoogle,
		OwnerEmail:    "test@example.com",
		FileHash:      "testhash",
		HashAlgorithm: "MD5",
		FileName:      "test.txt",
		FileSize:      100,
		CreatedOn:     time.Now(),
		LastModified:  time.Now(),
		LastSynced:    time.Now(),
	}

	if err := db.UpsertFile(testFile); err != nil {
		return fmt.Errorf("failed to test database write: %w", err)
	}

	if err := db.DeleteFile("test", model.ProviderGoogle); err != nil {
		return fmt.Errorf("failed to test database delete: %w", err)
	}

	return nil
}
