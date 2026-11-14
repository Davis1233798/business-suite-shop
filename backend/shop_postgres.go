package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/stdlib"
)

var registerShopPostgresDriver sync.Once

type shopPostgresDriver struct{ base driver.Driver }

func (d *shopPostgresDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.base.Open(name)
	if err != nil {
		return nil, err
	}
	return &shopPostgresConn{Conn: conn}, nil
}

type shopPostgresConn struct{ driver.Conn }

func (c *shopPostgresConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.Conn.Prepare(postgresPlaceholders(query))
	if err != nil {
		return nil, err
	}
	return &shopPostgresStmt{Stmt: stmt}, nil
}

func (c *shopPostgresConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	query = postgresPlaceholders(query)
	if conn, ok := c.Conn.(driver.ConnPrepareContext); ok {
		stmt, err := conn.PrepareContext(ctx, query)
		if err != nil {
			return nil, err
		}
		return &shopPostgresStmt{Stmt: stmt}, nil
	}
	return c.Prepare(query)
}

func (c *shopPostgresConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if conn, ok := c.Conn.(driver.ConnBeginTx); ok {
		return conn.BeginTx(ctx, opts)
	}
	if opts.ReadOnly || opts.Isolation != driver.IsolationLevel(sql.LevelDefault) {
		return nil, errors.New("PostgreSQL driver does not support requested transaction options")
	}
	return c.Conn.Begin()
}

func (c *shopPostgresConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	query = postgresPlaceholders(query)
	if conn, ok := c.Conn.(driver.ExecerContext); ok {
		return conn.ExecContext(ctx, query, args)
	}
	stmt, err := c.Prepare(query)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()
	return stmt.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (c *shopPostgresConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	query = postgresPlaceholders(query)
	if conn, ok := c.Conn.(driver.QueryerContext); ok {
		return conn.QueryContext(ctx, query, args)
	}
	stmt, err := c.Prepare(query)
	if err != nil {
		return nil, err
	}
	return stmt.(driver.StmtQueryContext).QueryContext(ctx, args)
}

func (c *shopPostgresConn) Ping(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.Pinger); ok {
		return conn.Ping(ctx)
	}
	return nil
}

func (c *shopPostgresConn) ResetSession(ctx context.Context) error {
	if conn, ok := c.Conn.(driver.SessionResetter); ok {
		return conn.ResetSession(ctx)
	}
	return nil
}

func (c *shopPostgresConn) IsValid() bool {
	if conn, ok := c.Conn.(driver.Validator); ok {
		return conn.IsValid()
	}
	return true
}

type shopPostgresStmt struct{ driver.Stmt }

func (s *shopPostgresStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if stmt, ok := s.Stmt.(driver.StmtExecContext); ok {
		return stmt.ExecContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i := range args {
		values[i] = args[i].Value
	}
	return s.Stmt.Exec(values)
}

func (s *shopPostgresStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if stmt, ok := s.Stmt.(driver.StmtQueryContext); ok {
		return stmt.QueryContext(ctx, args)
	}
	values := make([]driver.Value, len(args))
	for i := range args {
		values[i] = args[i].Value
	}
	return s.Stmt.Query(values)
}

// postgresPlaceholders rewrites the Shop repository's SQLite-style bind markers
// while leaving question marks inside SQL literals and comments untouched.
func postgresPlaceholders(query string) string {
	var out strings.Builder
	out.Grow(len(query) + 8)
	placeholder := 0
	quote := byte(0)
	lineComment, blockComment := false, false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if lineComment {
			out.WriteByte(ch)
			if ch == '\n' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			out.WriteByte(ch)
			if ch == '*' && i+1 < len(query) && query[i+1] == '/' {
				out.WriteByte(query[i+1])
				i++
				blockComment = false
			}
			continue
		}
		if quote != 0 {
			out.WriteByte(ch)
			if ch == quote {
				if i+1 < len(query) && query[i+1] == quote {
					out.WriteByte(query[i+1])
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		if ch == '-' && i+1 < len(query) && query[i+1] == '-' {
			out.WriteString("--")
			i++
			lineComment = true
			continue
		}
		if ch == '/' && i+1 < len(query) && query[i+1] == '*' {
			out.WriteString("/*")
			i++
			blockComment = true
			continue
		}
		if ch == '\'' || ch == '"' {
			quote = ch
			out.WriteByte(ch)
			continue
		}
		if ch == '?' {
			placeholder++
			fmt.Fprintf(&out, "$%d", placeholder)
			continue
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func isShopPostgresDB(db *sql.DB) bool {
	_, ok := db.Driver().(*shopPostgresDriver)
	return ok
}

func shopTimestampPredicate(db *sql.DB, column, operator string) string {
	if isShopPostgresDB(db) {
		return fmt.Sprintf("CAST(%s AS TIMESTAMPTZ) %s CURRENT_TIMESTAMP", column, operator)
	}
	return fmt.Sprintf("datetime(%s) %s CURRENT_TIMESTAMP", column, operator)
}

// openShopPostgresDB opens a Shop PostgreSQL store. The application entry point
// uses SUITE_DATABASE_URL to opt into it; an unset URL retains SQLite behavior.
func openShopPostgresDB(dsn string) (*sql.DB, error) {
	registerShopPostgresDriver.Do(func() {
		sql.Register("shop-postgres", &shopPostgresDriver{base: stdlib.GetDefaultDriver()})
	})
	db, err := sql.Open("shop-postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(8)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if err = migrateShopPostgres(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func openConfiguredDB(sqliteFile string) (*sql.DB, error) {
	if dsn := strings.TrimSpace(os.Getenv("SUITE_DATABASE_URL")); dsn != "" {
		return openShopPostgresDB(dsn)
	}
	return openDB(sqliteFile)
}

func migrateShopPostgres(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?), hashtext(?))", "shop-repository-migrations", "shop"); err != nil {
		return fmt.Errorf("lock Shop migrations: %w", err)
	}
	if _, err = tx.Exec(shopPostgresInitialSchema); err != nil {
		return fmt.Errorf("Shop PostgreSQL migration: %w", err)
	}
	if _, err = tx.Exec(`INSERT INTO shop_repository_schema(version) SELECT 2 WHERE NOT EXISTS (SELECT 1 FROM shop_repository_schema)`); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE shop_repository_schema SET version=2 WHERE version<2`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

//go:embed migrations/shop_postgres_initial.sql
var shopPostgresMigrationFS embed.FS

var shopPostgresInitialSchema = func() string {
	content, err := shopPostgresMigrationFS.ReadFile("migrations/shop_postgres_initial.sql")
	if err != nil {
		panic(err)
	}
	return string(content)
}()
