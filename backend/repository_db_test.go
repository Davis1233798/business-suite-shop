package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestRepositoryDatabaseIsolationAndLegacyRefusal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repository.sqlite")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mes_products", "erp_items", "pos_sales", "crm_leads", "dining_orders"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("foreign table %s: count=%d err=%v", table, count, err)
		}
	}
	db.Close()
	db, err = openDB(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	db.Close()
	legacy := filepath.Join(t.TempDir(), "legacy.sqlite")
	raw, err := sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec("CREATE TABLE organizations(id TEXT); CREATE TABLE legacy_data(value TEXT); INSERT INTO legacy_data VALUES ('keep')"); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if db, err = openDB(legacy); err == nil {
		db.Close()
		t.Fatal("legacy database accepted")
	}
	raw, err = sql.Open("sqlite", legacy)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var value string
	if err = raw.QueryRow("SELECT value FROM legacy_data").Scan(&value); err != nil || value != "keep" {
		t.Fatalf("legacy changed: %q %v", value, err)
	}
}
