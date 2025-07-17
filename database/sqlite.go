package database

import (
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteDB struct {
	conn *sql.DB
}

func (db *SQLiteDB) InitDB(path string) error {
	conn, err := sql.Open("sqlite3", path)
	if err != nil {
		return err
	}
	_, err = conn.Exec(`CREATE TABLE IF NOT EXISTS files (
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
	if err != nil {
		return err
	}
	db.conn = conn
	return nil
}

func (db *SQLiteDB) InsertOrUpdateFile(file FileRecord) error {
	_, err := db.conn.Exec(`INSERT INTO files (FileID, Provider, OwnerEmail, FileHash, FileName, FileSize, FileExtension, ParentFolderID, ParentFolderName, CreatedOn, LastModified, LastSynced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(FileID, Provider) DO UPDATE SET FileHash=excluded.FileHash, FileName=excluded.FileName, FileSize=excluded.FileSize, FileExtension=excluded.FileExtension, ParentFolderID=excluded.ParentFolderID, ParentFolderName=excluded.ParentFolderName, CreatedOn=excluded.CreatedOn, LastModified=excluded.LastModified, LastSynced=excluded.LastSynced, OwnerEmail=excluded.OwnerEmail`,
		file.FileID, file.Provider, file.OwnerEmail, file.FileHash, file.FileName, file.FileSize, file.FileExtension, file.ParentFolderID, file.ParentFolderName, file.CreatedOn, file.LastModified, file.LastSynced)
	return err
}

func (db *SQLiteDB) GetDuplicates(provider string) (map[string][]FileRecord, error) {
	rows, err := db.conn.Query(`SELECT FileHash, FileID, OwnerEmail, FileName, CreatedOn FROM files WHERE Provider=? GROUP BY FileHash HAVING COUNT(*) > 1`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string][]FileRecord)
	for rows.Next() {
		var hash, fileID, owner, name, created string
		if err := rows.Scan(&hash, &fileID, &owner, &name, &created); err != nil {
			return nil, err
		}
		result[hash] = append(result[hash], FileRecord{FileID: fileID, OwnerEmail: owner, FileName: name, CreatedOn: created})
	}
	return result, nil
}

func (db *SQLiteDB) GetFilesByHash(hash string) ([]FileRecord, error) {
	rows, err := db.conn.Query(`SELECT FileID, Provider, OwnerEmail, FileName, CreatedOn FROM files WHERE FileHash=?`, hash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []FileRecord
	for rows.Next() {
		var file FileRecord
		if err := rows.Scan(&file.FileID, &file.Provider, &file.OwnerEmail, &file.FileName, &file.CreatedOn); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func (db *SQLiteDB) GetLargestFiles(provider, ownerEmail string, limit int) ([]FileRecord, error) {
	rows, err := db.conn.Query(`SELECT FileID, FileSize FROM files WHERE Provider=? AND OwnerEmail=? ORDER BY FileSize DESC LIMIT ?`, provider, ownerEmail, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []FileRecord
	for rows.Next() {
		var file FileRecord
		if err := rows.Scan(&file.FileID, &file.FileSize); err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func (db *SQLiteDB) UpdateOwner(fileID, provider, newOwner string) error {
	_, err := db.conn.Exec(`UPDATE files SET OwnerEmail=? WHERE FileID=? AND Provider=?`, newOwner, fileID, provider)
	return err
}

func (db *SQLiteDB) Close() error {
	return db.conn.Close()
}
