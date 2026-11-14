package main

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestShopSQLiteSnapshotRejectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source #1.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE TABLE fixture (id INTEGER PRIMARY KEY); INSERT INTO fixture VALUES (1)")
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("prepare snapshot: %v / %v", err, closeErr)
	}

	source, err := openShopSQLiteSnapshot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var count int
	if err := source.QueryRow("SELECT count(*) FROM fixture").Scan(&count); err != nil || count != 1 {
		t.Fatalf("snapshot read: %d (%v)", count, err)
	}
	if _, err := source.Exec("INSERT INTO fixture VALUES (2)"); err == nil {
		t.Fatal("read-only snapshot accepted write")
	}
}

func TestShopSQLiteSnapshotRequiresSupportedSchema(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := validateShopSQLiteSnapshot(ctx, db); err == nil {
		t.Fatal("unmarked database accepted")
	}
	if _, err := db.Exec("CREATE TABLE shop_repository_schema(version INTEGER NOT NULL); INSERT INTO shop_repository_schema VALUES (3)"); err != nil {
		t.Fatal(err)
	}
	if err := validateShopSQLiteSnapshot(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE shop_repository_schema SET version=4"); err != nil {
		t.Fatal(err)
	}
	if err := validateShopSQLiteSnapshot(ctx, db); err == nil {
		t.Fatal("unsupported database version accepted")
	}
}

func TestShopPortfolioSnapshotLayout(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, entry := range strings.Split(strings.TrimSpace(portfolioShopLayout), "\n") {
		table, layout, _ := strings.Cut(entry, ":")
		columns := strings.Split(layout, ",")
		for i, column := range columns {
			name, kind, _ := strings.Cut(column, ":")
			columns[i] = fmt.Sprintf("%q %s", name, kind)
		}
		if _, err := db.Exec("CREATE TABLE " + table + " (" + strings.Join(columns, ",") + ")"); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateShopSQLiteSnapshot(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("ALTER TABLE shop_orders ADD COLUMN unexpected TEXT"); err != nil {
		t.Fatal(err)
	}
	if err := validateShopSQLiteSnapshot(ctx, db); err == nil {
		t.Fatal("unexpected column accepted")
	}
}
