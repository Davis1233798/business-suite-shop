package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type shopDataMigrationManifest struct {
	TenantMappings map[string]string
	UserMappings   map[string]string
}

type shopDataMigrationResult struct {
	AlreadyApplied    bool
	StockReconciled   bool
	SourceFingerprint string
}

// validateShopMigrationMappings verifies that every destination exists before
// the transaction can copy any business records.
func validateShopMigrationMappings(ctx context.Context, tx *sql.Tx, manifest shopDataMigrationManifest) error {
	if len(manifest.TenantMappings) == 0 {
		return errors.New("at least one explicit Shop tenant mapping is required")
	}
	destinations := make(map[string]bool, len(manifest.TenantMappings))
	for source, destination := range manifest.TenantMappings {
		if source == "" || destination == "" || destinations[destination] {
			return errors.New("invalid or duplicate Shop tenant mapping")
		}
		destinations[destination] = true
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM organizations WHERE id=?)", destination).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("mapped Shop tenant does not exist")
		}
	}
	for source, destination := range manifest.UserMappings {
		if source == "" || destination == "" {
			return errors.New("invalid Shop user mapping")
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE id=?)", destination).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errors.New("mapped Shop user does not exist")
		}
	}
	return nil
}

type shopMigrationTable struct {
	name  string
	scope string
}

// Parents are copied before their dependent rows. Shared bookkeeping tables
// are limited to records belonging to Shop.
var shopMigrationTables = []shopMigrationTable{
	{name: "shop_categories"},
	{name: "shop_products"},
	{name: "shop_variants"},
	{name: "shop_promotions"},
	{name: "shop_orders"},
	{name: "shop_order_items"},
	{name: "shop_carts"},
	{name: "shop_cart_items"},
	{name: "shop_inventory_reservations"},
	{name: "shop_payment_attempts"},
	{name: "shop_returns"},
	{name: "shop_reviews"},
	{name: "shop_shipments"},
	{name: "idempotency_keys", scope: " AND endpoint LIKE 'shop%'"},
	{name: "outbox_events", scope: " AND event_type LIKE 'shop.%'"},
	{name: "audit_events", scope: " AND entity_type LIKE 'shop%'"},
}

func (table shopMigrationTable) readTenant(ctx context.Context, tx *sql.Tx, organization string) (*sql.Rows, error) {
	return tx.QueryContext(ctx, "SELECT * FROM "+table.name+" WHERE organization_id=?"+table.scope+" ORDER BY id", organization)
}

func remapShopMigrationValue(table, column string, value any, destination string, manifest shopDataMigrationManifest) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch column {
	case "organization_id":
		return destination, nil
	case "user_id", "actor_id":
		source, ok := value.(string)
		if !ok || manifest.UserMappings[source] == "" {
			return nil, errors.New("Shop row has an unmapped user")
		}
		return manifest.UserMappings[source], nil
	case "session_id":
		session, ok := value.(string)
		if !ok {
			return nil, errors.New("invalid Shop session identifier")
		}
		const prefix = "shop-go-checkout:"
		if table == "shop_inventory_reservations" && strings.HasPrefix(session, prefix) {
			user := manifest.UserMappings[strings.TrimPrefix(session, prefix)]
			if user == "" {
				return nil, errors.New("Shop reservation has an unmapped user")
			}
			return prefix + user, nil
		}
		hash := sha256.Sum256([]byte(destination + "\x00" + session))
		return "retired-" + hex.EncodeToString(hash[:]), nil
	}
	return value, nil
}

func migrateShopSQLiteData(path string, target *sql.DB, manifest shopDataMigrationManifest) (shopDataMigrationResult, error) {
	ctx := context.Background()
	var result shopDataMigrationResult
	if !isShopPostgresDB(target) {
		return result, errors.New("Shop migration requires PostgreSQL")
	}
	db, err := openShopSQLiteSnapshot(ctx, path)
	if err != nil {
		return result, err
	}
	defer db.Close()
	if err = validateShopSQLiteSnapshot(ctx, db); err != nil {
		return result, err
	}
	source, err := db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer source.Rollback()
	hash := sha256.New()
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return result, err
	}
	hash.Write(manifestJSON)
	encoder := json.NewEncoder(hash)
	organizations := make([]string, 0, len(manifest.TenantMappings))
	for org := range manifest.TenantMappings {
		organizations = append(organizations, org)
	}
	sort.Strings(organizations)
	type batch struct {
		table   string
		columns []string
		records [][]any
	}
	var batches []batch
	for _, table := range shopMigrationTables {
		for _, org := range organizations {
			rows, err := table.readTenant(ctx, source, org)
			if err != nil {
				return result, fmt.Errorf("read %s: %w", table.name, err)
			}
			columns, err := rows.Columns()
			if err != nil {
				rows.Close()
				return result, err
			}
			for _, column := range columns {
				if strings.Trim(column, "abcdefghijklmnopqrstuvwxyz_0123456789") != "" || column == "" {
					rows.Close()
					return result, errors.New("invalid source column")
				}
			}
			b := batch{table: table.name, columns: columns}
			for rows.Next() {
				values := make([]any, len(columns))
				pointers := make([]any, len(columns))
				for i := range values {
					pointers[i] = &values[i]
				}
				if err = rows.Scan(pointers...); err != nil {
					rows.Close()
					return result, err
				}
				if err = encoder.Encode([]any{table.name, columns, values}); err != nil {
					rows.Close()
					return result, err
				}
				for i, column := range columns {
					values[i], err = remapShopMigrationValue(table.name, column, values[i], manifest.TenantMappings[org], manifest)
					if err != nil {
						rows.Close()
						return result, err
					}
				}
				b.records = append(b.records, values)
			}
			if err = rows.Err(); err != nil {
				rows.Close()
				return result, err
			}
			rows.Close()
			batches = append(batches, b)
		}
	}
	result.SourceFingerprint = hex.EncodeToString(hash.Sum(nil))
	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext('shop_data_migration'))"); err != nil {
		return result, err
	}
	if err = validateShopMigrationMappings(ctx, tx, manifest); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS shop_data_migration_runs (fingerprint TEXT PRIMARY KEY)"); err != nil {
		return result, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM shop_data_migration_runs WHERE fingerprint=?)", result.SourceFingerprint).Scan(&result.AlreadyApplied); err != nil {
		return result, err
	}
	if !result.AlreadyApplied {
		for _, b := range batches {
			marks := strings.TrimSuffix(strings.Repeat("?,", len(b.columns)), ",")
			query := "INSERT INTO " + b.table + " (" + strings.Join(b.columns, ",") + ") VALUES (" + marks + ")"
			for _, values := range b.records {
				if _, err = tx.ExecContext(ctx, query, values...); err != nil {
					return result, fmt.Errorf("import %s: %w", b.table, err)
				}
			}
		}
		for _, b := range batches {
			var fields []string
			switch b.table {
			case "shop_variants":
				fields = []string{"stock_quantity", "reserved_quantity"}
			case "shop_orders":
				fields = []string{"total_amount"}
			default:
				continue
			}
			columnIndex := make(map[string]int, len(b.columns))
			for i, column := range b.columns {
				columnIndex[column] = i
			}
			for _, values := range b.records {
				for _, field := range fields {
					var actual int64
					expected, ok := values[columnIndex[field]].(int64)
					query := "SELECT " + field + " FROM " + b.table + " WHERE id=? AND organization_id=?"
					err = tx.QueryRowContext(ctx, query, values[columnIndex["id"]], values[columnIndex["organization_id"]]).Scan(&actual)
					if err != nil || !ok || actual != expected {
						return result, fmt.Errorf("Shop reconciliation failed for %s.%s", b.table, field)
					}
				}
			}
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO shop_data_migration_runs (fingerprint) VALUES (?)", result.SourceFingerprint); err != nil {
			return result, err
		}
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	result.StockReconciled = true
	return result, nil
}
