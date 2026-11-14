package main

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// openShopSQLiteSnapshot opens an existing snapshot without creating or migrating it.
func openShopSQLiteSnapshot(ctx context.Context, path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("an existing regular Shop SQLite snapshot is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid Shop SQLite snapshot path")
	}
	uriPath := filepath.ToSlash(absolute)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	source := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", source.String())
	if err != nil {
		return nil, errors.New("could not open Shop SQLite snapshot")
	}
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, errors.New("could not read Shop SQLite snapshot")
	}
	return db, nil
}

func validateShopSQLiteSnapshot(ctx context.Context, db *sql.DB) error {
	var marker bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE name='shop_repository_schema')").Scan(&marker); err != nil {
		return err
	}
	if !marker {
		return validatePortfolioShopSnapshot(ctx, db)
	}
	var count, version int
	if err := db.QueryRowContext(ctx, "SELECT count(*), COALESCE(MAX(version), 0) FROM shop_repository_schema").Scan(&count, &version); err != nil || count != 1 || version != 3 {
		return errors.New("Shop SQLite snapshot must use repository schema version 3")
	}
	return nil
}
