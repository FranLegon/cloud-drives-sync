package database

import (
	"database/sql"

	"crypto/sha256"
	"fmt"
	"io"

	_ "github.com/mattn/go-sqlite3"
)

// HashReader computes the SHA256 hash of the contents of an io.Reader and returns it as a hex string.
func HashReader(r io.Reader) string {
	h := sha256.New()
	io.Copy(h, r)
	return fmt.Sprintf("%x", h.Sum(nil))
}

type FileRecord struct {
	FileID           string
	Provider         string
	OwnerEmail       string
	FileHash         string
	FileName         string
	FileSize         int64
	FileExtension    string
	ParentFolderID   string
	ParentFolderName string
	CreatedOn        string
	LastModified     string
	LastSynced       string
}

type Database struct {
	db *sql.DB
}

func InitDB(path string) error {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE IF NOT EXISTS files (
	FileID TEXT,
	Provider TEXT,
	OwnerEmail TEXT,
	FileHash TEXT,
	FileName TEXT,
	FileSize INTEGER,
	FileExtension TEXT,
	ParentFolderID TEXT,
	ParentFolderName TEXT,
	CreatedOn DATETIME,
	LastModified DATETIME,
	LastSynced DATETIME,
	PRIMARY KEY (FileID, Provider)
)`)
	return err
}

func OpenDB(path string) (*Database, error) {
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	return &Database{db: db}, nil
}

func (d *Database) Close() error {
	return d.db.Close()
}

func (d *Database) UpsertFile(f FileRecord) error {
	_, err := d.db.Exec(`
INSERT INTO files (FileID, Provider, OwnerEmail, FileHash, FileName, FileSize, FileExtension, ParentFolderID, ParentFolderName, CreatedOn, LastModified, LastSynced)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(FileID, Provider) DO UPDATE SET
	FileHash=excluded.FileHash,
	FileName=excluded.FileName,
	FileSize=excluded.FileSize,
	FileExtension=excluded.FileExtension,
	ParentFolderID=excluded.ParentFolderID,
	ParentFolderName=excluded.ParentFolderName,
	CreatedOn=excluded.CreatedOn,
	LastModified=excluded.LastModified,
	LastSynced=excluded.LastSynced
`, f.FileID, f.Provider, f.OwnerEmail, f.FileHash, f.FileName, f.FileSize, f.FileExtension, f.ParentFolderID, f.ParentFolderName, f.CreatedOn, f.LastModified, f.LastSynced)
	return err
}

func (d *Database) DeleteFileRecord(fileID, provider string) error {
	_, err := d.db.Exec(`DELETE FROM files WHERE FileID=? AND Provider=?`, fileID, provider)
	return err
}

func (d *Database) FindDuplicates() (map[string][]FileRecord, error) {
	rows, err := d.db.Query(`
SELECT FileID, Provider, OwnerEmail, FileHash, FileName, FileSize, FileExtension, ParentFolderID, ParentFolderName, CreatedOn, LastModified, LastSynced
FROM files
WHERE FileHash IN (
	SELECT FileHash FROM files GROUP BY FileHash, Provider HAVING COUNT(*) > 1
)
ORDER BY Provider, FileHash, CreatedOn
`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dups := map[string][]FileRecord{}
	for rows.Next() {
		var f FileRecord
		err := rows.Scan(&f.FileID, &f.Provider, &f.OwnerEmail, &f.FileHash, &f.FileName, &f.FileSize, &f.FileExtension, &f.ParentFolderID, &f.ParentFolderName, &f.CreatedOn, &f.LastModified, &f.LastSynced)
		if err != nil {
			return nil, err
		}
		dups[f.FileHash] = append(dups[f.FileHash], f)
	}
	return dups, nil
}

func (d *Database) GetFilesByProvider(provider, owner string) ([]FileRecord, error) {
	rows, err := d.db.Query(`SELECT FileID, Provider, OwnerEmail, FileHash, FileName, FileSize, FileExtension, ParentFolderID, ParentFolderName, CreatedOn, LastModified, LastSynced FROM files WHERE Provider=? AND OwnerEmail=?`, provider, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		rows.Scan(&f.FileID, &f.Provider, &f.OwnerEmail, &f.FileHash, &f.FileName, &f.FileSize, &f.FileExtension, &f.ParentFolderID, &f.ParentFolderName, &f.CreatedOn, &f.LastModified, &f.LastSynced)
		out = append(out, f)
	}
	return out, nil
}

func (d *Database) GetLargestFilesNotInOtherAccounts(provider, email string) ([]FileRecord, error) {
	rows, err := d.db.Query(`
SELECT FileID, Provider, OwnerEmail, FileHash, FileName, FileSize, FileExtension, ParentFolderID, ParentFolderName, CreatedOn, LastModified, LastSynced
FROM files
WHERE Provider=? AND OwnerEmail=? AND FileHash NOT IN (
	SELECT FileHash FROM files WHERE Provider=? AND OwnerEmail!=?
)
ORDER BY FileSize DESC
`, provider, email, provider, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileRecord
	for rows.Next() {
		var f FileRecord
		rows.Scan(&f.FileID, &f.Provider, &f.OwnerEmail, &f.FileHash, &f.FileName, &f.FileSize, &f.FileExtension, &f.ParentFolderID, &f.ParentFolderName, &f.CreatedOn, &f.LastModified, &f.LastSynced)
		out = append(out, f)
	}
	return out, nil
}

func (d *Database) UpdateOwnerEmail(fileID, provider, newEmail string) error {
	_, err := d.db.Exec(`UPDATE files SET OwnerEmail=? WHERE FileID=? AND Provider=?`, newEmail, fileID, provider)
	return err
}
