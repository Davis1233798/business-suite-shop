package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func postgresShopTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("SHOP_POSTGRES_TEST_URL")
	if dsn == "" {
		t.Skip("set SHOP_POSTGRES_TEST_URL to a disposable PostgreSQL database")
	}
	db, err := openShopPostgresDB(dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL Shop database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

type postgresShopCatalog struct {
	orgID, userID, variantID string
}

func seedPostgresShopCatalog(t *testing.T, db *sql.DB, stock int) postgresShopCatalog {
	t.Helper()
	ids := postgresShopCatalog{orgID: token(), userID: token(), variantID: token()}
	categoryID, productID := token(), token()
	for _, stmt := range []struct {
		query string
		args  []any
	}{
		{"INSERT INTO organizations(id,name,slug) VALUES(?,?,?)", []any{ids.orgID, "PostgreSQL test", "pg-" + ids.orgID}},
		{"INSERT INTO users(id,email,display_name) VALUES(?,?,?)", []any{ids.userID, ids.userID + "@example.invalid", "PG test"}},
		{"INSERT INTO shop_categories(id,organization_id,name,slug) VALUES(?,?,?,?)", []any{categoryID, ids.orgID, "PG category", "pg-" + categoryID}},
		{"INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", []any{productID, ids.orgID, "PG product", "pg-" + productID, "test", categoryID}},
		{"INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,?,0,1)", []any{ids.variantID, ids.orgID, productID, "SKU-" + ids.variantID, "Unit", 725, stock}},
	} {
		if _, err := db.Exec(stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed PostgreSQL catalog: %v", err)
		}
	}
	return ids
}

func postgresCheckoutInput(key string) checkoutInput {
	return checkoutInput{VariantID: "", Quantity: 0, Items: []checkoutItemInput{{VariantID: key, Quantity: 1}}, Key: "request-" + key, Email: "buyer@example.invalid", Address: map[string]any{"name": "Buyer", "city": "Taipei", "address": "Test street", "phone": "0900000000"}}
}

func TestShopPostgresMigrationRestartAndCheckoutIsolation(t *testing.T) {
	db := postgresShopTestDB(t)
	orgA := seedPostgresShopCatalog(t, db, 1)
	orgB := seedPostgresShopCatalog(t, db, 1)
	inputA := postgresCheckoutInput(orgA.variantID)
	inputB := postgresCheckoutInput(orgB.variantID)
	inputA.Key = "same-tenant-scoped-key"
	inputB.Key = inputA.Key

	resultA, err := checkoutOrder(t.Context(), db, orgA.orgID, orgA.userID, inputA)
	if err != nil {
		t.Fatalf("first tenant checkout: %v", err)
	}
	replay, err := checkoutOrder(t.Context(), db, orgA.orgID, orgA.userID, inputA)
	if err != nil || replay["orderId"] != resultA["orderId"] || replay["duplicate"] != true {
		t.Fatalf("checkout replay: result=%#v err=%v", replay, err)
	}
	resultB, err := checkoutOrder(t.Context(), db, orgB.orgID, orgB.userID, inputB)
	if err != nil || resultB["orderId"] == resultA["orderId"] {
		t.Fatalf("same request key in another organization: result=%#v err=%v", resultB, err)
	}

	// Reopening runs the idempotent migration against persisted data, like an app restart.
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("SHOP_POSTGRES_TEST_URL")
	restarted, err := openShopPostgresDB(dsn)
	if err != nil {
		t.Fatalf("restart against existing PostgreSQL database: %v", err)
	}
	defer restarted.Close()
	var orders int
	if err = restarted.QueryRow("SELECT count(*) FROM shop_orders WHERE organization_id=?", orgA.orgID).Scan(&orders); err != nil || orders != 1 {
		t.Fatalf("persisted rows after migration restart: orders=%d err=%v", orders, err)
	}
	var stockA, stockB int
	if err = restarted.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=? AND organization_id=?", orgA.variantID, orgA.orgID).Scan(&stockA); err != nil {
		t.Fatal(err)
	}
	if err = restarted.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=? AND organization_id=?", orgB.variantID, orgB.orgID).Scan(&stockB); err != nil {
		t.Fatal(err)
	}
	if stockA != 0 || stockB != 0 {
		t.Fatalf("tenant stock isolation failed: A=%d B=%d", stockA, stockB)
	}
}

func TestShopPostgresConcurrentCheckoutCannotOversell(t *testing.T) {
	db := postgresShopTestDB(t)
	catalog := seedPostgresShopCatalog(t, db, 1)
	const attempts = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, soldOut, unexpected := 0, 0, []error{}
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := postgresCheckoutInput(catalog.variantID)
			input.Key = fmt.Sprintf("concurrent-%02d", i)
			_, err := checkoutOrder(t.Context(), db, catalog.orgID, catalog.userID, input)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, errCheckoutSoldOut):
				soldOut++
			default:
				unexpected = append(unexpected, err)
			}
		}(i)
	}
	wg.Wait()
	if accepted != 1 || soldOut != attempts-1 || len(unexpected) != 0 {
		t.Fatalf("concurrent checkout results: accepted=%d soldOut=%d unexpected=%v", accepted, soldOut, unexpected)
	}
	var stock, orders int
	if err := db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=?", catalog.variantID).Scan(&stock); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT count(*) FROM shop_orders WHERE organization_id=?", catalog.orgID).Scan(&orders); err != nil {
		t.Fatal(err)
	}
	if stock != 0 || orders != 1 {
		t.Fatalf("oversell or partial write: stock=%d orders=%d", stock, orders)
	}
}

func TestShopPostgresPaymentOutboxFailureRollsBack(t *testing.T) {
	db := postgresShopTestDB(t)
	catalog := seedPostgresShopCatalog(t, db, 1)
	input := postgresCheckoutInput(catalog.variantID)
	input.Key = "payment-rollback"
	checkout, err := checkoutOrder(t.Context(), db, catalog.orgID, catalog.userID, input)
	if err != nil {
		t.Fatal(err)
	}
	constraint := "block_paid_outbox_" + token()[:12]
	if _, err = db.Exec("ALTER TABLE outbox_events ADD CONSTRAINT " + constraint + " CHECK (organization_id <> '" + catalog.orgID + "' OR event_type <> 'shop.paid')"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("ALTER TABLE outbox_events DROP CONSTRAINT " + constraint) })
	callback := paymentCallbackInput{OrderID: checkout["orderId"].(string), AttemptReference: "pg-rollback", Amount: 785, Status: "success"}
	if _, err = simulateShopPayment(db, catalog.orgID, catalog.userID, "admin", callback, time.Now().UTC()); err == nil {
		t.Fatal("expected outbox constraint failure")
	}
	var paymentStatus string
	var attempts, activeReservations, outbox int
	for _, check := range []struct {
		query string
		args  []any
		dest  any
	}{
		{"SELECT payment_status FROM shop_orders WHERE id=?", []any{callback.OrderID}, &paymentStatus},
		{"SELECT count(*) FROM shop_payment_attempts WHERE organization_id=? AND order_id=?", []any{catalog.orgID, callback.OrderID}, &attempts},
		{"SELECT count(*) FROM shop_inventory_reservations WHERE organization_id=? AND cart_id=? AND status='active'", []any{catalog.orgID, callback.OrderID}, &activeReservations},
		{"SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='shop.paid'", []any{catalog.orgID}, &outbox},
	} {
		if err = db.QueryRow(check.query, check.args...).Scan(check.dest); err != nil {
			t.Fatal(err)
		}
	}
	if paymentStatus != "pending" || attempts != 0 || activeReservations != 1 || outbox != 0 {
		t.Fatalf("payment transaction partially committed: payment=%s attempts=%d reservations=%d outbox=%d", paymentStatus, attempts, activeReservations, outbox)
	}
}

func TestShopPostgresCompetingPaymentCallbacksHaveOnePaidTransition(t *testing.T) {
	db := postgresShopTestDB(t)
	catalog := seedPostgresShopCatalog(t, db, 1)
	input := postgresCheckoutInput(catalog.variantID)
	input.Key = "payment-competition"
	checkout, err := checkoutOrder(t.Context(), db, catalog.orgID, catalog.userID, input)
	if err != nil {
		t.Fatal(err)
	}
	orderID := checkout["orderId"].(string)
	const competitors = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	callbackErrors := []error{}
	for i := 0; i < competitors; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			callback := paymentCallbackInput{OrderID: orderID, AttemptReference: fmt.Sprintf("pg-success-%02d", i), Amount: 785, Status: "success"}
			result, callbackErr := simulateShopPayment(db, catalog.orgID, catalog.userID, "admin", callback, time.Now().UTC())
			mu.Lock()
			defer mu.Unlock()
			if callbackErr != nil {
				callbackErrors = append(callbackErrors, callbackErr)
				return
			}
			if result.PaymentStatus != "paid" {
				callbackErrors = append(callbackErrors, fmt.Errorf("callback returned payment status %q", result.PaymentStatus))
			}
		}(i)
	}
	wg.Wait()
	if len(callbackErrors) != 0 {
		t.Fatalf("competing callback errors: %v", callbackErrors)
	}
	var paymentStatus, reservationStatus string
	var outbox, attempts int
	for _, check := range []struct {
		query string
		args  []any
		dest  any
	}{
		{"SELECT payment_status FROM shop_orders WHERE id=? AND organization_id=?", []any{orderID, catalog.orgID}, &paymentStatus},
		{"SELECT status FROM shop_inventory_reservations WHERE cart_id=? AND organization_id=?", []any{orderID, catalog.orgID}, &reservationStatus},
		{"SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='shop.paid'", []any{catalog.orgID}, &outbox},
		{"SELECT count(*) FROM shop_payment_attempts WHERE organization_id=? AND order_id=?", []any{catalog.orgID, orderID}, &attempts},
	} {
		if err = db.QueryRow(check.query, check.args...).Scan(check.dest); err != nil {
			t.Fatal(err)
		}
	}
	if paymentStatus != "paid" || reservationStatus != "consumed" || outbox != 1 || attempts != competitors {
		t.Fatalf("competing payment transition: payment=%s reservation=%s outbox=%d attempts=%d", paymentStatus, reservationStatus, outbox, attempts)
	}
}
