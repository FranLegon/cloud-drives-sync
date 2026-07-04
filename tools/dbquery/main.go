package main

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"

	_ "github.com/mutecomm/go-sqlcipher/v4"
)

func main() {
	pass := os.Getenv("SYNC_CLOUD_DRIVES_PASS")
	if pass == "" {
		fmt.Println("SYNC_CLOUD_DRIVES_PASS not set")
		os.Exit(1)
	}
	connStr := fmt.Sprintf("file:cloud-drives-sync-metadata.db?_pragma_key=%s&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000", url.QueryEscape(pass))
	db, err := sql.Open("sqlite3", connStr)
	if err != nil {
		panic(err)
	}
	defer db.Close()

	fmt.Println("=== FILES (logical) ===")
	rows, err := db.Query(`SELECT path, name, status FROM files ORDER BY status, path`)
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		var path, name, status string
		rows.Scan(&path, &name, &status)
		fmt.Printf("[%-12s] %s\n", status, path)
	}
	rows.Close()

	fmt.Println("\n=== FILE STATUS COUNTS ===")
	rows, _ = db.Query(`SELECT status, COUNT(*) FROM files GROUP BY status`)
	for rows.Next() {
		var status string
		var c int
		rows.Scan(&status, &c)
		fmt.Printf("%-12s %d\n", status, c)
	}
	rows.Close()

	fmt.Println("\n=== REPLICAS by provider/status (active, by path location) ===")
	rows, _ = db.Query(`
		SELECT provider, status,
			SUM(CASE WHEN path LIKE '%soft-deleted%' THEN 1 ELSE 0 END) as in_soft,
			SUM(CASE WHEN path NOT LIKE '%soft-deleted%' THEN 1 ELSE 0 END) as in_root
		FROM replicas GROUP BY provider, status ORDER BY provider, status`)
	for rows.Next() {
		var provider, status string
		var inSoft, inRoot int
		rows.Scan(&provider, &status, &inSoft, &inRoot)
		fmt.Printf("%-10s %-10s soft-deleted=%d  active-area=%d\n", provider, status, inSoft, inRoot)
	}
	rows.Close()

	fmt.Println("\n=== ACTIVE replicas NOT in soft-deleted (these are 'at root/active') ===")
	rows, _ = db.Query(`
		SELECT provider, account_id, path FROM replicas
		WHERE status='active' AND path NOT LIKE '%soft-deleted%'
		ORDER BY provider, path`)
	n := 0
	for rows.Next() {
		var provider, account, path string
		rows.Scan(&provider, &account, &path)
		fmt.Printf("%-10s %-30s %s\n", provider, account, path)
		n++
	}
	rows.Close()
	fmt.Printf("TOTAL active-area replicas: %d\n", n)
}
