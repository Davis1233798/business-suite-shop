package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestShopCheckoutRouteUsesRealTenantScopedOrderTransaction(t *testing.T) {
	a := appForTest(t)
	space := token()
	admin := login(t, a, "admin@suite.test", space)
	var baselineOrders int
	var me struct {
		Data Identity `json:"data"`
	}
	if w := request(a, "GET", "/api/auth/me?site=shop", nil, admin...); w.Code != 200 {
		t.Fatal(w.Body.String())
	} else {
		_ = json.Unmarshal(w.Body.Bytes(), &me)
	}
	if e := a.db.QueryRow("SELECT count(*) FROM shop_orders WHERE organization_id=?", me.Data.OrganizationID).Scan(&baselineOrders); e != nil {
		t.Fatal(e)
	}
	product, variant := token(), token()
	if _, e := a.db.Exec("INSERT INTO shop_categories(id,organization_id,name,slug) VALUES(?,?,?,?)", token(), me.Data.OrganizationID, "Checkout", "checkout-"+product); e != nil {
		t.Fatal(e)
	}
	category := "checkout-" + product
	var categoryID string
	if e := a.db.QueryRow("SELECT id FROM shop_categories WHERE organization_id=? AND slug=?", me.Data.OrganizationID, category).Scan(&categoryID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", product, me.Data.OrganizationID, "Server priced", "product-"+product, "", categoryID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,2,1,1)", variant, me.Data.OrganizationID, product, "SKU-"+variant, "One unit", 725); e != nil {
		t.Fatal(e)
	}
	reservedAttempt := map[string]any{"variantId": variant, "quantity": 2, "idempotencyKey": "reserved-stock-key", "customerEmail": "buyer@example.test", "shippingAddress": map[string]string{"name": "Buyer", "city": "Taipei", "address": "Demo", "phone": "0912345678"}}
	if w := request(a, "POST", "/api/stations/shop/checkout", reservedAttempt, admin...); w.Code != 409 {
		t.Fatalf("checkout consumed reserved inventory: %d %s", w.Code, w.Body.String())
	}
	payload := map[string]any{"variantId": variant, "quantity": 1, "idempotencyKey": "checkout-route-key", "customerEmail": "buyer@example.test", "shippingAddress": map[string]string{"name": "Buyer", "city": "Taipei", "address": "Demo", "phone": "0912345678"}}
	w := request(a, "POST", "/api/stations/shop/checkout", payload, admin...)
	if w.Code != 200 {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Data["totalAmount"] != float64(785) || result.Data["shippingFee"] != float64(60) {
		t.Fatalf("checkout did not use server price: %#v", result.Data)
	}
	first := result.Data["orderId"]
	w = request(a, "POST", "/api/stations/shop/checkout", payload, admin...)
	if w.Code != 200 {
		t.Fatalf("idempotent replay: %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.Data["orderId"] != first {
		t.Fatalf("replay created a second order: %#v", result.Data)
	}
	changed := map[string]any{"variantId": variant, "quantity": 2, "idempotencyKey": "checkout-route-key", "customerEmail": "buyer@example.test", "shippingAddress": map[string]string{"name": "Buyer", "city": "Taipei", "address": "Demo", "phone": "0912345678"}}
	if w = request(a, "POST", "/api/stations/shop/checkout", changed, admin...); w.Code != 409 {
		t.Fatalf("idempotency key with changed request: %d %s", w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/stations/shop/checkout", payload, login(t, a, "admin@suite.test", token())...); w.Code != 404 {
		t.Fatalf("cross-organization variant checkout: %d %s", w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/stations/shop/checkout", payload, loginSite(t, a, "office@suite.test", space, "shop")...); w.Code != 409 {
		t.Fatalf("same-organization other-user replay: %d %s", w.Code, w.Body.String())
	}
	readonly := loginSite(t, a, "viewer@suite.test", space, "shop")
	if w = request(a, "POST", "/api/stations/shop/checkout", payload, readonly...); w.Code != 403 {
		t.Fatalf("readonly checkout: %d %s", w.Code, w.Body.String())
	}
	var count, stock, reservations int
	_ = a.db.QueryRow("SELECT count(*) FROM shop_orders WHERE organization_id=?", me.Data.OrganizationID).Scan(&count)
	_ = a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=?", variant).Scan(&stock)
	_ = a.db.QueryRow("SELECT count(*) FROM shop_inventory_reservations WHERE organization_id=? AND variant_id=?", me.Data.OrganizationID, variant).Scan(&reservations)
	if count != baselineOrders+1 || stock != 1 || reservations != 1 {
		t.Fatalf("orders=%d baseline=%d stock=%d reservations=%d", count, baselineOrders, stock, reservations)
	}
	var lineTotal, subtotal, shippingFee, total int
	if e := a.db.QueryRow(`SELECT i.line_total,o.subtotal,o.shipping_fee,o.total_amount FROM shop_order_items i JOIN shop_orders o ON o.id=i.order_id WHERE i.order_id=?`, first).Scan(&lineTotal, &subtotal, &shippingFee, &total); e != nil {
		t.Fatal(e)
	}
	if lineTotal != 725 || subtotal != 725 || shippingFee != 60 || total != 785 {
		t.Fatalf("line/order totals line=%d subtotal=%d shipping=%d total=%d", lineTotal, subtotal, shippingFee, total)
	}
	if _, e := a.db.Exec("UPDATE shop_inventory_reservations SET expires_at=? WHERE variant_id=?", "2000-01-01T00:00:00Z", variant); e != nil {
		t.Fatal(e)
	}
	if e := releaseExpiredShopReservations(a.db, me.Data.OrganizationID, time.Now()); e != nil {
		t.Fatal(e)
	}
	var releasedStock int
	var reservationStatus string
	_ = a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=?", variant).Scan(&releasedStock)
	_ = a.db.QueryRow("SELECT status FROM shop_inventory_reservations WHERE variant_id=?", variant).Scan(&reservationStatus)
	if releasedStock != 2 || reservationStatus != "expired" {
		t.Fatalf("reservation release stock=%d status=%s", releasedStock, reservationStatus)
	}
}

func TestShopCheckoutLabCreatesRealOrdersWithoutOversell(t *testing.T) {
	a := appForTest(t)
	admin := login(t, a, "admin@suite.test", token())
	var baselineOrders int
	if e := a.db.QueryRow("SELECT count(*) FROM shop_orders").Scan(&baselineOrders); e != nil {
		t.Fatal(e)
	}
	w := request(a, http.MethodPost, "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 5}, admin...)
	if w.Code != 200 {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	var prepared struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &prepared) != nil || prepared.Data.RunID == "" {
		t.Fatal(w.Body.String())
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, duplicates, soldOut := 0, 0, 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("buyer-%d", i)
			if i%4 == 0 {
				key = fmt.Sprintf("buyer-%d", i-1)
			}
			res := request(a, http.MethodPost, "/api/stations/shop/checkout-lab", map[string]any{"action": "checkout", "runId": prepared.Data.RunID, "key": key}, admin...)
			var body struct {
				Data struct {
					Outcome string `json:"outcome"`
					OrderID string `json:"orderId"`
				} `json:"data"`
			}
			_ = json.Unmarshal(res.Body.Bytes(), &body)
			mu.Lock()
			defer mu.Unlock()
			if res.Code != 200 {
				t.Errorf("checkout: %d %s", res.Code, res.Body.String())
				return
			}
			switch body.Data.Outcome {
			case "accepted":
				accepted++
				if body.Data.OrderID == "" {
					t.Error("accepted without order")
				}
			case "duplicate":
				duplicates++
			case "sold_out":
				soldOut++
			default:
				t.Errorf("outcome %q", body.Data.Outcome)
			}
		}(i)
	}
	wg.Wait()
	if accepted != 5 || duplicates == 0 || soldOut == 0 {
		t.Fatalf("accepted=%d duplicate=%d soldout=%d", accepted, duplicates, soldOut)
	}
	var orders int
	if e := a.db.QueryRow("SELECT count(*) FROM shop_orders").Scan(&orders); e != nil || orders != baselineOrders {
		t.Fatalf("lab contaminated workspace orders=%d baseline=%d err=%v", orders, baselineOrders, e)
	}
	lab := a.checkoutLabForTest(prepared.Data.RunID)
	if e := lab.db.QueryRow("SELECT count(*) FROM shop_orders").Scan(&orders); e != nil || orders != 5 {
		t.Fatalf("isolated real order count=%d err=%v", orders, e)
	}
	var stock int
	if e := lab.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=?", lab.variantID).Scan(&stock); e != nil || stock != 0 {
		t.Fatalf("isolated remaining stock=%d err=%v", stock, e)
	}
}

func TestShopCheckoutLabExpiryClosesTemporaryDatabases(t *testing.T) {
	a := appForTest(t)
	admin := login(t, a, "admin@suite.test", token())
	w := request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 1}, admin...)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var d struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	lab := a.checkoutLabForTest(d.Data.RunID)
	a.cleanupCheckoutLabs(lab.createdAt.Add(checkoutLabTTL + 1))
	if _, ok := a.checkoutLabForTestMaybe(d.Data.RunID); ok {
		t.Fatal("expired lab database was retained")
	}
	if err := lab.db.Ping(); err == nil {
		t.Fatal("expired lab database was not closed")
	}
}

func TestShopCheckoutLabCapsRunsBeforeAllocatingMore(t *testing.T) {
	a := appForTest(t)
	admin := login(t, a, "admin@suite.test", token())
	for i := 0; i < checkoutLabMaxPerOrganization; i++ {
		w := request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 1}, admin...)
		if w.Code != 200 {
			t.Fatalf("prepare %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if w := request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 1}, admin...); w.Code != 429 {
		t.Fatalf("run limit: %d %s", w.Code, w.Body.String())
	}
	a.labMu.Lock()
	defer a.labMu.Unlock()
	if len(a.checkoutLabs) != checkoutLabMaxPerOrganization {
		t.Fatalf("retained lab count=%d", len(a.checkoutLabs))
	}
}

func TestShopCheckoutLabPermissionAndTenantIsolation(t *testing.T) {
	a := appForTest(t)
	admin := login(t, a, "admin@suite.test", token())
	other := login(t, a, "admin@suite.test", token())
	viewer := login(t, a, "viewer@suite.test", token())
	w := request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 2}, admin...)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var d struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w = request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "checkout", "runId": d.Data.RunID, "key": "x"}, other...); w.Code != 404 {
		t.Fatalf("cross tenant: %d", w.Code)
	}
	if w = request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 2}, viewer...); w.Code != 403 {
		t.Fatalf("readonly: %d", w.Code)
	}
}
