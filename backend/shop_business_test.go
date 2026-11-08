package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func TestShopBusinessCheckoutPaymentShippingConcurrency(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	front := loginSite(t, a, "front@suite.test", space, "shop")
	viewer := loginSite(t, a, "viewer@suite.test", space, "shop")
	var variant string
	if e := a.db.QueryRow("SELECT id FROM shop_variants WHERE organization_id=? AND stock_quantity=1 LIMIT 1", org).Scan(&variant); e != nil {
		t.Fatal(e)
	}
	path := "/api/stations/shop/business"
	snapshot := func() map[string]int64 {
		w := request(a, "GET", "/api/stations/shop/operations-snapshot", nil, admin...)
		var value struct {
			Data map[string]int64 `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &value); e != nil || w.Code != 200 {
			t.Fatalf("snapshot %d %v", w.Code, e)
		}
		return value.Data
	}
	before := snapshot()
	requestFor := func(i int) map[string]any {
		return map[string]any{"action": "checkout", "variantId": variant, "quantity": 1, "idempotencyKey": fmt.Sprintf("checkout-%08d", i)}
	}
	if w := request(a, "POST", path, requestFor(99), viewer...); w.Code != http.StatusForbidden {
		t.Fatalf("readonly checkout: %d", w.Code)
	}
	other := loginSite(t, a, "admin@suite.test", token(), "shop")
	if w := request(a, "POST", path, requestFor(99), other...); w.Code != http.StatusNotFound {
		t.Fatalf("cross tenant: %d", w.Code)
	}
	var wg sync.WaitGroup
	type outcome struct {
		code  int
		body  []byte
		index int
	}
	results := make(chan outcome, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := request(a, "POST", path, requestFor(i), front...)
			results <- outcome{w.Code, w.Body.Bytes(), i}
		}(i)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	var winner outcome
	for x := range results {
		switch x.code {
		case 200:
			success++
			winner = x
		case 409:
			conflicts++
		default:
			t.Errorf("unexpected checkout %d: %s", x.code, x.body)
		}
	}
	if success != 1 || conflicts != 31 {
		t.Fatalf("checkout race: %d success, %d conflicts", success, conflicts)
	}
	var envelope struct {
		Data struct {
			OrderID string `json:"orderId"`
			Total   int64  `json:"totalAmount"`
		} `json:"data"`
	}
	if e := json.Unmarshal(winner.body, &envelope); e != nil || envelope.Data.OrderID == "" {
		t.Fatal("missing order")
	}
	order := envelope.Data.OrderID
	if s := snapshot(); s["pendingPayment"] != before["pendingPayment"]+1 {
		t.Fatalf("checkout snapshot %+v before %+v", s, before)
	}
	replay := request(a, "POST", path, requestFor(winner.index), front...)
	if replay.Code != 200 {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	var again struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	json.Unmarshal(replay.Body.Bytes(), &again)
	if again.Data.OrderID != order {
		t.Fatal("retry produced a different order")
	}
	wrong := requestFor(winner.index)
	wrong["quantity"] = 2
	if w := request(a, "POST", path, wrong, front...); w.Code != 409 {
		t.Fatalf("changed retry payload: %d", w.Code)
	}
	payment := map[string]any{"action": "pay", "orderId": order, "idempotencyKey": "payment-00000001"}
	if w := request(a, "POST", path, payment, other...); w.Code != 404 {
		t.Fatalf("foreign payment: %d", w.Code)
	}
	if w := request(a, "POST", path, payment, front...); w.Code != 200 {
		t.Fatalf("pay: %d %s", w.Code, w.Body.String())
	}
	if s := snapshot(); s["pendingPayment"] != before["pendingPayment"] || s["paidUnfulfilled"] != before["paidUnfulfilled"]+1 || s["pendingOutbox"] != before["pendingOutbox"]+1 {
		t.Fatalf("payment snapshot %+v before %+v", s, before)
	}
	if w := request(a, "POST", path, payment, front...); w.Code != 200 {
		t.Fatalf("pay replay: %d", w.Code)
	}
	ship := map[string]any{"action": "ship", "orderId": order, "tracking": "DEMO-TRACK-01", "idempotencyKey": "shipping-0000001"}
	if w := request(a, "POST", path, ship, front...); w.Code != 403 {
		t.Fatalf("front ship: %d", w.Code)
	}
	if w := request(a, "POST", path, ship, admin...); w.Code != 200 {
		t.Fatalf("ship: %d %s", w.Code, w.Body.String())
	}
	if s := snapshot(); s["paidUnfulfilled"] != before["paidUnfulfilled"] {
		t.Fatalf("shipment snapshot %+v before %+v", s, before)
	}
	var stock, orders, events, payments, shipments, audits int
	var status string
	a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=? AND organization_id=?", variant, org).Scan(&stock)
	a.db.QueryRow("SELECT count(*) FROM shop_orders WHERE id=? AND organization_id=?", order, org).Scan(&orders)
	a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE id=? AND organization_id=?", "shop.paid:"+order, org).Scan(&events)
	a.db.QueryRow("SELECT count(*) FROM shop_payment_attempts WHERE order_id=? AND organization_id=?", order, org).Scan(&payments)
	a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE order_id=? AND organization_id=?", order, org).Scan(&shipments)
	a.db.QueryRow("SELECT count(*) FROM audit_events WHERE entity_id=? AND organization_id=?", order, org).Scan(&audits)
	a.db.QueryRow("SELECT status FROM shop_inventory_reservations WHERE cart_id=? AND organization_id=?", order, org).Scan(&status)
	if stock != 0 || orders != 1 || events != 1 || payments != 1 || shipments != 1 || audits != 3 || status != "consumed" {
		t.Fatalf("invalid persisted state stock=%d orders=%d events=%d payments=%d shipments=%d audits=%d status=%s", stock, orders, events, payments, shipments, audits, status)
	}
}
func TestShopExpiredReservationRestoresStockOnce(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var variant string
	a.db.QueryRow("SELECT id FROM shop_variants WHERE organization_id=? AND stock_quantity=1 LIMIT 1", org).Scan(&variant)
	w := request(a, "POST", "/api/stations/shop/business", map[string]any{"action": "checkout", "variantId": variant, "quantity": 1, "idempotencyKey": "expiry-00000001"}, admin...)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if _, e := a.db.Exec("UPDATE shop_inventory_reservations SET expires_at='2000-01-01T00:00:00Z' WHERE organization_id=? AND cart_id=?", org, body.Data.OrderID); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := a.expireShopReservations(); e != nil {
			t.Fatal(e)
		}
	}
	var stock int
	var payment, reservation string
	a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=?", variant).Scan(&stock)
	a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=?", body.Data.OrderID).Scan(&payment)
	a.db.QueryRow("SELECT status FROM shop_inventory_reservations WHERE cart_id=?", body.Data.OrderID).Scan(&reservation)
	if stock != 1 || payment != "expired" || reservation != "expired" {
		t.Fatalf("expiry stock=%d payment=%s reservation=%s", stock, payment, reservation)
	}
}

func TestShopFailedPaymentExpiryRestoresStockOnce(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var variant string
	a.db.QueryRow("SELECT id FROM shop_variants WHERE organization_id=? AND stock_quantity=1 LIMIT 1", org).Scan(&variant)
	w := request(a, "POST", "/api/stations/shop/business", map[string]any{"action": "checkout", "variantId": variant, "quantity": 1, "idempotencyKey": "expiry-00000001"}, admin...)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var body struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)
	if _, e := a.db.Exec("UPDATE shop_inventory_reservations SET expires_at='2000-01-01T00:00:00Z' WHERE organization_id=? AND cart_id=?", org, body.Data.OrderID); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("UPDATE shop_orders SET payment_status='failed' WHERE organization_id=? AND id=?", org, body.Data.OrderID); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- a.expireShopReservations() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var stock int
	var payment, reservation string
	a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=?", variant).Scan(&stock)
	a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=?", body.Data.OrderID).Scan(&payment)
	a.db.QueryRow("SELECT status FROM shop_inventory_reservations WHERE cart_id=?", body.Data.OrderID).Scan(&reservation)
	if stock != 1 || payment != "expired" || reservation != "expired" {
		t.Fatalf("expiry stock=%d payment=%s reservation=%s", stock, payment, reservation)
	}
	pay := request(a, "POST", "/api/stations/shop/business", map[string]any{"action": "pay", "orderId": body.Data.OrderID, "idempotencyKey": "expired-retry-01"}, admin...)
	if pay.Code != http.StatusConflict {
		t.Fatalf("expired payment retry: %d %s", pay.Code, pay.Body.String())
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=? AND organization_id=?", variant, org).Scan(&stock); err != nil || stock != 1 {
		t.Fatalf("stock after retry %d %v", stock, err)
	}
	if err := a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=? AND organization_id=?", body.Data.OrderID, org).Scan(&payment); err != nil || payment != "expired" {
		t.Fatalf("payment after retry %s %v", payment, err)
	}
	if err := a.db.QueryRow("SELECT status FROM shop_inventory_reservations WHERE cart_id=? AND organization_id=?", body.Data.OrderID, org).Scan(&reservation); err != nil || reservation != "expired" {
		t.Fatalf("reservation after retry %s %v", reservation, err)
	}
}

func TestShopFailedPaymentCanRetryBeforeExpiry(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	front := loginSite(t, a, "front@suite.test", space, "shop")
	var variant string
	if err := a.db.QueryRow("SELECT id FROM shop_variants WHERE organization_id=? AND stock_quantity=1 LIMIT 1", org).Scan(&variant); err != nil {
		t.Fatal(err)
	}
	path := "/api/stations/shop/business"
	checkout := request(a, "POST", path, map[string]any{"action": "checkout", "variantId": variant, "quantity": 1, "idempotencyKey": "retry-checkout-01"}, admin...)
	if checkout.Code != 200 {
		t.Fatal(checkout.Body.String())
	}
	var body struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(checkout.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE shop_orders SET payment_status='failed' WHERE organization_id=? AND id=?", org, body.Data.OrderID); err != nil {
		t.Fatal(err)
	}
	foreignPay := request(a, "POST", path, map[string]any{"action": "pay", "orderId": body.Data.OrderID, "idempotencyKey": "foreign-retry-01"}, front...)
	if foreignPay.Code != http.StatusForbidden {
		t.Fatalf("foreign retry %d %s", foreignPay.Code, foreignPay.Body.String())
	}
	pay := request(a, "POST", path, map[string]any{"action": "pay", "orderId": body.Data.OrderID, "idempotencyKey": "retry-payment-01"}, admin...)
	if pay.Code != 200 {
		t.Fatalf("retry %d %s", pay.Code, pay.Body.String())
	}
	var status string
	if err := a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=? AND organization_id=?", body.Data.OrderID, org).Scan(&status); err != nil || status != "paid" {
		t.Fatalf("payment %s %v", status, err)
	}
}
