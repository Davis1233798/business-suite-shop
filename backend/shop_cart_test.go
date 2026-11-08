package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func TestShopCartCheckoutAndWholeOrderExpiry(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	buyer := loginSite(t, a, "front@suite.test", space, "shop")
	viewer := loginSite(t, a, "viewer@suite.test", space, "shop")
	other := loginSite(t, a, "admin@suite.test", token(), "shop")
	path := "/api/stations/shop/cart-checkout"
	var scarce, common string
	var commonStock int64
	if e := a.db.QueryRow("SELECT id FROM shop_variants WHERE organization_id=? AND stock_quantity=1 LIMIT 1", org).Scan(&scarce); e != nil {
		t.Fatal(e)
	}
	if e := a.db.QueryRow("SELECT id,stock_quantity FROM shop_variants WHERE organization_id=? AND stock_quantity>1 LIMIT 1", org).Scan(&common, &commonStock); e != nil {
		t.Fatal(e)
	}
	input := func(i int) map[string]any {
		return map[string]any{"lines": []any{map[string]any{"variantId": common, "quantity": 2}, map[string]any{"variantId": scarce, "quantity": 1}}, "idempotencyKey": fmt.Sprintf("shop-cart-%08d", i)}
	}
	if w := request(a, "POST", path, input(99), viewer...); w.Code != 403 {
		t.Fatalf("readonly %d", w.Code)
	}
	if w := request(a, "POST", path, input(99), other...); w.Code != 404 {
		t.Fatalf("foreign variants %d", w.Code)
	}
	var wg sync.WaitGroup
	type outcome struct {
		code, index int
		body        []byte
	}
	results := make(chan outcome, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := request(a, "POST", path, input(i), buyer...)
			results <- outcome{w.Code, i, w.Body.Bytes()}
		}(i)
	}
	wg.Wait()
	close(results)
	ok, conflicts, winner := 0, 0, outcome{}
	for r := range results {
		if r.code == 200 {
			ok++
			winner = r
		} else if r.code == 409 {
			conflicts++
		} else {
			t.Errorf("checkout %d %s", r.code, r.body)
		}
	}
	if ok != 1 || conflicts != 11 {
		t.Fatalf("checkout %d conflicts %d", ok, conflicts)
	}
	if w := request(a, "POST", path, input(winner.index), buyer...); w.Code != 200 {
		t.Fatalf("replay %d", w.Code)
	}
	var v struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	json.Unmarshal(winner.body, &v)
	order := v.Data.OrderID
	var count int
	a.db.QueryRow("SELECT count(*) FROM shop_order_items WHERE organization_id=? AND order_id=?", org, order).Scan(&count)
	if count != 2 {
		t.Fatalf("items %d", count)
	}
	// An incomplete reservation must never be partially consumed and paid.
	if _, e := a.db.Exec("UPDATE shop_inventory_reservations SET status='cancelled' WHERE organization_id=? AND cart_id=? AND variant_id=?", org, order, common); e != nil {
		t.Fatal(e)
	}
	pay := map[string]any{"action": "pay", "orderId": order, "idempotencyKey": "cart-payment-0001"}
	if w := request(a, "POST", "/api/stations/shop/business", pay, buyer...); w.Code != 409 {
		t.Fatalf("incomplete payment %d %s", w.Code, w.Body.String())
	}
	a.db.Exec("UPDATE shop_inventory_reservations SET status='active' WHERE organization_id=? AND cart_id=? AND variant_id=?", org, order, common)
	a.db.Exec("UPDATE shop_inventory_reservations SET quantity=1 WHERE organization_id=? AND cart_id=? AND variant_id=?", org, order, common)
	if w := request(a, "POST", "/api/stations/shop/business", pay, buyer...); w.Code != 409 {
		t.Fatalf("mismatched quantity paid %d", w.Code)
	}
	a.db.Exec("UPDATE shop_inventory_reservations SET quantity=2 WHERE organization_id=? AND cart_id=? AND variant_id=?", org, order, common)
	// One expired line invalidates and releases every reservation, even future lines.
	a.db.Exec("UPDATE shop_inventory_reservations SET expires_at='2000-01-01T00:00:00Z' WHERE organization_id=? AND cart_id=? AND variant_id=?", org, order, scarce)
	for i := 0; i < 2; i++ {
		if e := a.expireShopReservations(); e != nil {
			t.Fatal(e)
		}
	}
	var scarceStock, newCommon int64
	var expired int
	var state string
	a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", org, scarce).Scan(&scarceStock)
	a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", org, common).Scan(&newCommon)
	a.db.QueryRow("SELECT count(*) FROM shop_inventory_reservations WHERE organization_id=? AND cart_id=? AND status='expired'", org, order).Scan(&expired)
	a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE organization_id=? AND id=?", org, order).Scan(&state)
	if scarceStock != 1 || newCommon != commonStock || expired != 2 || state != "expired" {
		t.Fatalf("expiry %d %d %d %s", scarceStock, newCommon, expired, state)
	}
	w := request(a, "POST", path, input(200), buyer...)
	if w.Code != 200 {
		t.Fatalf("second checkout %d %s", w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	pay["orderId"] = v.Data.OrderID
	pay["idempotencyKey"] = "cart-payment-0002"
	if w := request(a, "POST", "/api/stations/shop/business", pay, buyer...); w.Code != 200 {
		t.Fatalf("pay all lines %d %s", w.Code, w.Body.String())
	}
	a.db.QueryRow("SELECT count(*) FROM shop_inventory_reservations WHERE organization_id=? AND cart_id=? AND status='consumed'", org, v.Data.OrderID).Scan(&count)
	if count != 2 {
		t.Fatalf("consumed %d", count)
	}
}
