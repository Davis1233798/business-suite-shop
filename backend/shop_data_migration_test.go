package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func shopMigrationPostgresTarget(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("SHOP_POSTGRES_TEST_URL")
	if dsn == "" {
		t.Skip("set SHOP_POSTGRES_TEST_URL to a disposable PostgreSQL database")
	}
	admin, err := openShopPostgresDB(dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "migration_" + token()[:16]
	if _, err = admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	if err = admin.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := openShopPostgresDB(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		cleanup, cleanupErr := openShopPostgresDB(dsn)
		if cleanupErr == nil {
			_, _ = cleanup.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)
			_ = cleanup.Close()
		}
	})
	return db, parsed.String()
}

type shopMigrationSource struct {
	orgID, userID, categoryID, productID, variantID, orderID, itemID, cartID string
}

func createShopMigrationSource(t *testing.T) (string, shopMigrationSource) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shop-source.sqlite")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ids := shopMigrationSource{
		orgID: token(), userID: token(), categoryID: token(), productID: token(),
		variantID: token(), orderID: token(), itemID: token(), cartID: token(),
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", []any{ids.orgID, "Legacy Shop tenant", "legacy-" + ids.orgID}},
		{"INSERT INTO users(id,email,display_name) VALUES(?,?,?)", []any{ids.userID, "legacy@example.invalid", "Legacy customer"}},
		{"INSERT INTO station_accounts(user_id,organization_id,email,password_hash) VALUES(?,?,?,?)", []any{ids.userID, ids.orgID, "legacy@example.invalid", "MUST_NOT_COPY_PASSWORD_HASH"}},
		{"INSERT INTO station_sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", []any{token(), ids.userID, 9999999999999}},
		{"INSERT INTO shop_categories(id,organization_id,name,slug) VALUES(?,?,?,?)", []any{ids.categoryID, ids.orgID, "Legacy category", "legacy-category"}},
		{"INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", []any{ids.productID, ids.orgID, "Legacy product", "legacy-product", "Source fixture", ids.categoryID}},
		{"INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,125,17,2,1)", []any{ids.variantID, ids.orgID, ids.productID, "LEGACY-SKU", "Legacy unit"}},
		{"INSERT INTO shop_promotions(id,organization_id,code,discount_type,discount_value,min_spend,is_active) VALUES(?,?,?,'fixed',25,100,1)", []any{token(), ids.orgID, "LEGACY25"}},
		{"INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,discount_amount,shipping_fee,total_amount,payment_status,fulfillment_status,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", []any{ids.orderID, ids.orgID, "LEGACY-ORDER-1", ids.userID, "buyer@example.invalid", `{"name":"Buyer"}`, 250, 25, 60, 285, "paid", "shipped", "legacy-request"}},
		{"INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,?,?)", []any{ids.itemID, ids.orgID, ids.orderID, ids.variantID, "Legacy product", "Legacy unit", "LEGACY-SKU", 125, 2, 250}},
		{"INSERT INTO shop_inventory_reservations(id,organization_id,variant_id,session_id,cart_id,quantity,expires_at,status) VALUES(?,?,?,?,?,?,?,'consumed')", []any{token(), ids.orgID, ids.variantID, "shop-go-checkout:" + ids.userID, ids.orderID, 2, "2027-01-01T00:00:00Z"}},
		{"INSERT INTO shop_carts(id,organization_id,session_id,user_id,coupon_code) VALUES(?,?,?,?,?)", []any{ids.cartID, ids.orgID, "legacy-session-secret", ids.userID, "LEGACY25"}},
		{"INSERT INTO shop_cart_items(id,organization_id,cart_id,variant_id,quantity) VALUES(?,?,?,?,?)", []any{token(), ids.orgID, ids.cartID, ids.variantID, 2}},
		{"INSERT INTO shop_payment_attempts(id,organization_id,order_id,attempt_reference,amount,provider,status,actor_id,actor_role) VALUES(?,?,?,?,?,'legacy','success',?,'admin')", []any{token(), ids.orgID, ids.orderID, "legacy-payment", 285, ids.userID}},
		{"INSERT INTO shop_returns(id,organization_id,order_id,order_item_id,quantity,reason,status,refund_amount) VALUES(?,?,?,?,1,'legacy return','refunded',125)", []any{token(), ids.orgID, ids.orderID, ids.itemID}},
		{"INSERT INTO shop_reviews(id,organization_id,product_id,user_id,order_id,rating,comment,is_visible) VALUES(?,?,?,?,?,5,'Legacy review',1)", []any{token(), ids.orgID, ids.productID, ids.userID, ids.orderID}},
		{"INSERT INTO shop_shipments(id,organization_id,order_id,tracking_number,carrier,status) VALUES(?,?,?,'LEGACY-TRACK','Legacy carrier','shipped')", []any{token(), ids.orgID, ids.orderID}},
		{"INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,200,?)", []any{token(), ids.orgID, "legacy-request", "shop_checkout", "hash", `{"orderId":"` + ids.orderID + `"}`}},
		{"INSERT INTO outbox_events(id,organization_id,event_type,payload,status,retry_count) VALUES(?,?,?,?,'pending',1)", []any{token(), ids.orgID, "shop.paid", `{"orderId":"` + ids.orderID + `"}`}},
		{"INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,after_value) VALUES(?,?,?,'admin','shop.test','shop_order',?,?)", []any{token(), ids.orgID, ids.userID, ids.orderID, `{"status":"paid"}`}},
	}
	for _, statement := range statements {
		if _, err = db.Exec(statement.query, statement.args...); err != nil {
			db.Close()
			t.Fatalf("seed SQLite Shop source: %v\nSQL: %s", err, statement.query)
		}
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, ids
}

func TestShopSQLiteImportMapsTenantAndUsersWithoutAuthSecrets(t *testing.T) {
	target, dsn := shopMigrationPostgresTarget(t)
	sourcePath, source := createShopMigrationSource(t)
	destOrg, destUser := "dest-"+source.orgID, "dest-"+source.userID
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", []any{destOrg, "Mapped destination", "dest-" + source.orgID}},
		{"INSERT INTO users(id,email,display_name) VALUES(?,?,?)", []any{destUser, "destination@example.invalid", "Destination user"}},
		{"INSERT INTO station_accounts(user_id,organization_id,email,password_hash) VALUES(?,?,?,?)", []any{destUser, destOrg, "destination@example.invalid", "TARGET_AUTH_SECRET_MUST_REMAIN"}},
	} {
		if _, err := target.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	manifest := shopDataMigrationManifest{TenantMappings: map[string]string{source.orgID: destOrg}, UserMappings: map[string]string{source.userID: destUser}}
	input, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := runShopMigration([]string{"--apply", sourcePath}, bytes.NewReader(input), &output, dsn); err != nil {
		t.Fatal(err)
	}
	var result shopDataMigrationResult
	err = json.Unmarshal(output.Bytes(), &result)
	if err != nil {
		t.Fatalf("migrate Shop records: %v", err)
	}
	if result.AlreadyApplied || !result.StockReconciled || result.SourceFingerprint == "" {
		t.Fatalf("migration result lacks reconciliation evidence: %#v", result)
	}
	var orders, items, variants, reviews, returns, attempts, shipments, outbox, idempotency, audit int
	for _, check := range []struct {
		query string
		arg   any
		dest  *int
	}{
		{"SELECT count(*) FROM shop_orders WHERE organization_id=?", destOrg, &orders},
		{"SELECT count(*) FROM shop_order_items WHERE organization_id=?", destOrg, &items},
		{"SELECT count(*) FROM shop_variants WHERE organization_id=?", destOrg, &variants},
		{"SELECT count(*) FROM shop_reviews WHERE organization_id=?", destOrg, &reviews},
		{"SELECT count(*) FROM shop_returns WHERE organization_id=?", destOrg, &returns},
		{"SELECT count(*) FROM shop_payment_attempts WHERE organization_id=?", destOrg, &attempts},
		{"SELECT count(*) FROM shop_shipments WHERE organization_id=?", destOrg, &shipments},
		{"SELECT count(*) FROM outbox_events WHERE organization_id=?", destOrg, &outbox},
		{"SELECT count(*) FROM idempotency_keys WHERE organization_id=?", destOrg, &idempotency},
		{"SELECT count(*) FROM audit_events WHERE organization_id=?", destOrg, &audit},
	} {
		if err = target.QueryRow(check.query, check.arg).Scan(check.dest); err != nil {
			t.Fatal(err)
		}
	}
	if orders != 1 || items != 1 || variants != 1 || reviews != 1 || returns != 1 || attempts != 1 || shipments != 1 || outbox != 1 || idempotency != 1 || audit != 1 {
		t.Fatalf("imported row counts differ: orders=%d items=%d variants=%d reviews=%d returns=%d payments=%d shipments=%d outbox=%d idem=%d audit=%d", orders, items, variants, reviews, returns, attempts, shipments, outbox, idempotency, audit)
	}
	var org, owner, sku, reservationSession, cartSession string
	var stock, reserved int64
	if err = target.QueryRow("SELECT organization_id,user_id FROM shop_orders WHERE id=?", source.orderID).Scan(&org, &owner); err != nil {
		t.Fatal(err)
	}
	if org != destOrg || owner != destUser {
		t.Fatalf("tenant/user mapping was not applied: org=%s owner=%s", org, owner)
	}
	if err = target.QueryRow("SELECT organization_id,sku,stock_quantity,reserved_quantity FROM shop_variants WHERE id=?", source.variantID).Scan(&org, &sku, &stock, &reserved); err != nil {
		t.Fatal(err)
	}
	if org != destOrg || sku != "LEGACY-SKU" || stock != 17 || reserved != 2 {
		t.Fatalf("variant reconciliation mismatch: org=%s sku=%s stock=%d reserved=%d", org, sku, stock, reserved)
	}
	if err = target.QueryRow("SELECT session_id FROM shop_inventory_reservations WHERE cart_id=?", source.orderID).Scan(&reservationSession); err != nil {
		t.Fatal(err)
	}
	if reservationSession != "shop-go-checkout:"+destUser {
		t.Fatalf("reservation user session was not mapped: %q", reservationSession)
	}
	if err = target.QueryRow("SELECT session_id FROM shop_carts WHERE id=?", source.cartID).Scan(&cartSession); err != nil {
		t.Fatal(err)
	}
	if cartSession == "legacy-session-secret" || cartSession == "" {
		t.Fatalf("legacy cart session secret was copied verbatim: %q", cartSession)
	}
	var sourceAuthRows, copiedLegacyUsers, targetAuthRows int
	_ = sourceAuthRows
	if err = target.QueryRow("SELECT count(*) FROM users WHERE id=?", source.userID).Scan(&copiedLegacyUsers); err != nil {
		t.Fatal(err)
	}
	if err = target.QueryRow("SELECT count(*) FROM station_accounts WHERE user_id=? AND password_hash='TARGET_AUTH_SECRET_MUST_REMAIN'", destUser).Scan(&targetAuthRows); err != nil {
		t.Fatal(err)
	}
	var copiedSessions int
	if err = target.QueryRow("SELECT count(*) FROM station_sessions WHERE user_id=?", destUser).Scan(&copiedSessions); err != nil {
		t.Fatal(err)
	}
	if copiedLegacyUsers != 0 || targetAuthRows != 1 || copiedSessions != 0 {
		t.Fatalf("auth records were copied or altered: legacyUsers=%d targetAuth=%d sessions=%d", copiedLegacyUsers, targetAuthRows, copiedSessions)
	}
}

func TestShopSQLiteImportRollsBackAtomicallyAndReplays(t *testing.T) {
	target, _ := shopMigrationPostgresTarget(t)
	sourcePath, source := createShopMigrationSource(t)
	destOrg, destUser := "dest-"+source.orgID, "dest-"+source.userID
	if _, err := target.Exec("INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", destOrg, "Mapped destination", "dest-"+source.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec("INSERT INTO users(id,email,display_name) VALUES(?,?,?)", destUser, "destination@example.invalid", "Destination user"); err != nil {
		t.Fatal(err)
	}
	manifest := shopDataMigrationManifest{TenantMappings: map[string]string{source.orgID: destOrg}, UserMappings: map[string]string{source.userID: destUser}}
	if _, err := target.Exec("ALTER TABLE shop_returns ADD CONSTRAINT migration_failure_check CHECK (reason <> 'legacy return')"); err != nil {
		t.Fatal(err)
	}
	if _, err := migrateShopSQLiteData(sourcePath, target, manifest); err == nil {
		t.Fatal("expected injected constraint failure")
	}
	var imported, ledgerRows int
	if err := target.QueryRow("SELECT count(*) FROM shop_categories WHERE organization_id=?", destOrg).Scan(&imported); err != nil {
		t.Fatal(err)
	}
	if err := target.QueryRow("SELECT count(*) FROM shop_data_migration_runs").Scan(&ledgerRows); err == nil {
		if err = target.QueryRow("SELECT count(*) FROM shop_data_migration_runs").Scan(&ledgerRows); err != nil {
			t.Fatal(err)
		}
	} else if err.Error() != `ERROR: relation "shop_data_migration_runs" does not exist (SQLSTATE 42P01)` {
		t.Fatal(err)
	}
	if imported != 0 || ledgerRows != 0 {
		t.Fatalf("failed import was partial: categoryRows=%d ledgerRows=%d", imported, ledgerRows)
	}
	if _, err := target.Exec("ALTER TABLE shop_returns DROP CONSTRAINT migration_failure_check"); err != nil {
		t.Fatal(err)
	}
	first, err := migrateShopSQLiteData(sourcePath, target, manifest)
	if err != nil || first.AlreadyApplied {
		t.Fatalf("retry after rollback: result=%#v err=%v", first, err)
	}
	second, err := migrateShopSQLiteData(sourcePath, target, manifest)
	if err != nil || !second.AlreadyApplied || second.SourceFingerprint != first.SourceFingerprint {
		t.Fatalf("replay result=%#v err=%v", second, err)
	}
	if err = target.QueryRow("SELECT count(*) FROM shop_orders WHERE organization_id=?", destOrg).Scan(&imported); err != nil || imported != 1 {
		t.Fatalf("replay duplicated records: rows=%d err=%v", imported, err)
	}
}

func TestShopPortfolioImportPreservesLegacyOrders(t *testing.T) {
	target, dsn := shopMigrationPostgresTarget(t)
	path, source := createShopMigrationSource(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("DROP TABLE shop_repository_schema; ALTER TABLE shop_orders DROP COLUMN coupon_code; ALTER TABLE shop_payment_attempts DROP COLUMN actor_id; ALTER TABLE shop_payment_attempts DROP COLUMN actor_role")
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("prepare legacy fixture: %v / %v", err, closeErr)
	}
	if _, err := target.Exec("INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", source.orgID, "Imported tenant", source.orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec("INSERT INTO users(id,email,display_name) VALUES(?,?,?)", source.userID, "import@example.invalid", "Imported user"); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(shopDataMigrationManifest{TenantMappings: map[string]string{source.orgID: source.orgID}, UserMappings: map[string]string{source.userID: source.userID}})
	if err != nil {
		t.Fatal(err)
	}
	var result bytes.Buffer
	if err := runShopMigration([]string{"--apply", path}, bytes.NewReader(manifest), &result, dsn); err != nil {
		t.Fatal(err)
	}
	var total, stock, reserved int
	var coupon sql.NullString
	if err := target.QueryRow("SELECT total_amount,coupon_code FROM shop_orders WHERE id=?", source.orderID).Scan(&total, &coupon); err != nil || total != 285 || coupon.Valid {
		t.Fatalf("legacy order: total=%d coupon=%v err=%v", total, coupon, err)
	}
	if err := target.QueryRow("SELECT stock_quantity,reserved_quantity FROM shop_variants WHERE id=?", source.variantID).Scan(&stock, &reserved); err != nil || stock != 17 || reserved != 2 {
		t.Fatalf("legacy stock: %d/%d err=%v", stock, reserved, err)
	}
	var actor, role string
	if err := target.QueryRow("SELECT actor_id,actor_role FROM shop_payment_attempts WHERE order_id=?", source.orderID).Scan(&actor, &role); err != nil || actor != "" || role != "" {
		t.Fatalf("legacy payment actor: %q/%q err=%v", actor, role, err)
	}
}
