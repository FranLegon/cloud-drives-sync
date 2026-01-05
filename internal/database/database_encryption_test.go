package database

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDatabaseEncryption verifies that the database is properly encrypted
func TestDatabaseEncryption(t *testing.T) {
	// Create a temporary directory for testing
	testDir := t.TempDir()
	
	password := "testPassword123!"
	wrongPassword := "wrongPassword456!"
	
	// Override the executable path by setting the environment or by creating the DB directly in testDir
	dbPath := filepath.Join(testDir, DBFileName)
	
	// We need to temporarily override GetDBPath behavior for testing
	// Save original working directory and executable
	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	
	// The executable will be in a temp directory during tests
	// We'll create the database in testDir and then reference it
	t.Logf("Test directory: %s", testDir)
	t.Logf("Expected DB path: %s", dbPath)
	t.Logf("GetDBPath returns: %s", GetDBPath())
	
	expectedPath := GetDBPath()
	expectedDir := filepath.Dir(expectedPath)
	
	// Create directory if it doesn't exist
	os.MkdirAll(expectedDir, 0755)
	
	// Clean up at the end of all subtests
	defer os.Remove(expectedPath)
	
	t.Run("CreateEncryptedDatabase", func(t *testing.T) {
		err := CreateDB(password)
		if err != nil {
			t.Fatalf("Failed to create encrypted database: %v", err)
		}
		
		// Verify database file exists at the expected path
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Fatalf("Database file was not created at %s", expectedPath)
		}
	})
	
	t.Run("OpenWithCorrectPassword", func(t *testing.T) {
		db, err := Open(password)
		if err != nil {
			t.Fatalf("Failed to open database with correct password: %v", err)
		}
		defer db.Close()
		
		// Initialize schema to verify we can perform operations
		err = db.Initialize()
		if err != nil {
			t.Fatalf("Failed to initialize schema: %v", err)
		}
	})
	
	t.Run("RejectWrongPassword", func(t *testing.T) {
		db, err := Open(wrongPassword)
		if err == nil {
			db.Close()
			t.Fatal("Database opened with wrong password (should have failed)")
		}
		t.Logf("Correctly rejected wrong password: %v", err)
	})
	
	t.Run("RejectEmptyPassword", func(t *testing.T) {
		db, err := Open("")
		if err == nil {
			db.Close()
			t.Fatal("Database opened without password (should have failed)")
		}
		t.Logf("Correctly rejected empty password: %v", err)
	})
}

// TestDatabaseOperationsWithEncryption verifies database operations work with encryption
func TestDatabaseOperationsWithEncryption(t *testing.T) {
	password := "operationsTestPassword!"
	
	// Get the expected database path
	expectedPath := GetDBPath()
	expectedDir := filepath.Dir(expectedPath)
	
	t.Logf("Expected database path: %s", expectedPath)
	t.Logf("Expected directory: %s", expectedDir)
	
	// Create directory if it doesn't exist
	os.MkdirAll(expectedDir, 0755)
	
	// Clean up any existing database
	os.Remove(expectedPath)
	
	// Create and initialize database
	t.Log("Creating database...")
	err := CreateDB(password)
	if err != nil {
		t.Fatalf("Failed to create database: %v", err)
	}
	defer os.Remove(expectedPath)
	
	// Verify file was created
	if info, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Fatalf("Database file was not created at %s", expectedPath)
	} else {
		t.Logf("Database file created, size: %d bytes", info.Size())
	}
	
	t.Log("Opening database...")
	db, err := Open(password)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()
	
	t.Log("Initializing schema...")
	err = db.Initialize()
	if err != nil {
		t.Fatalf("Failed to initialize schema: %v", err)
	}
	
	// Query to check if tables exist
	var count int
	err = db.conn.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('files', 'folders', 'files_fragments')").Scan(&count)
	if err != nil {
		t.Fatalf("Failed to query tables: %v", err)
	}
	if count != 3 {
		t.Fatalf("Expected 3 tables, got %d", count)
	}
	
	t.Logf("Successfully created and verified encrypted database with %d tables", count)
}
