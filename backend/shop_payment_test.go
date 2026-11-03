package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

type shopCheckoutResult struct {
	Data struct {
		OrderID     string `json:"orderId"`
		TotalAmount int    `json:"totalAmount"`
	} `json:"data"`
}

func createPaymentTestOrder(t *testing.T, a *App, cookies []*http.Cookie, organizationID, userID string) shopCheckoutResult {
	t.Helper()
	product, variant := token(), token()
	category := token()
	if _, err := a.db.Exec("INSERT INTO shop_categories(id,organization_id,name,slug) VALUES(?,?,?,?)", category, organizationID, "Payment test", "payment-test-"+category); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", product, organizationID, "Payment test product", "payment-test-"+product, "", category); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,2,0,1)", variant, organizationID, product, "PAY-"+variant, "Unit", 725); err != nil {
		t.Fatal(err)
	}
	w := request(a, http.MethodPost, "/api/stations/shop/checkout", map[string]any{"variantId": variant, "quantity": 1, "idempotencyKey": token(), "customerEmail": "buyer@example.test", "shippingAddress": map[string]string{"name": "Buyer", "city": "Taipei", "address": "Test", "phone": "0912345678"}}, cookies...)
	if w.Code != http.StatusOK {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	var result shopCheckoutResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || result.Data.OrderID == "" || result.Data.TotalAmount != 785 {
		t.Fatalf("checkout result: %#v err=%v", result, err)
	}
	var storedUser string
	if err := a.db.QueryRow("SELECT user_id FROM shop_orders WHERE id=?", result.Data.OrderID).Scan(&storedUser); err != nil || storedUser != userID {
		t.Fatalf("unexpected order owner %q err=%v", storedUser, err)
	}
	return result
}

func paymentCallback(a *App, cookies []*http.Cookie, orderID, ref, status string, amount int) (int, map[string]any) {
	w := request(a, http.MethodPost, "/api/stations/shop/payment", map[string]any{"orderId": orderID, "attemptReference": ref, "status": status, "amount": amount}, cookies...)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body.Data
}

func TestShopPaymentCallbackAtomicIdempotentTransitions(t *testing.T) {
	a := appForTest(t)
	space := token()
	admin := login(t, a, "admin@suite.test", space)
	var me struct{ Data Identity `json:"data"` }
	if w := request(a, http.MethodGet, "/api/auth/me?site=shop", nil, admin...); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	} else {
		_ = json.Unmarshal(w.Body.Bytes(), &me)
	}
	order := createPaymentTestOrder(t, a, admin, me.Data.OrganizationID, me.Data.UserID)
	if code, body := paymentCallback(a, admin, order.Data.OrderID, "attempt-success", "success", 784); code != http.StatusConflict {
		t.Fatalf("wrong amount should conflict: code=%d body=%#v", code, body)
	}
	if code, _ := paymentCallback(a, admin, order.Data.OrderID, "attempt-success", "success", order.Data.TotalAmount); code != http.StatusOK {
		t.Fatalf("success callback: %d", code)
	}
	if code, body := paymentCallback(a, admin, order.Data.OrderID, "attempt-success", "success", order.Data.TotalAmount); code != http.StatusOK || body["alreadyProcessed"] != true {
		t.Fatalf("same callback replay: code=%d body=%#v", code, body)
	}
	if code, _ := paymentCallback(a, admin, order.Data.OrderID, "attempt-success", "failed", order.Data.TotalAmount); code != http.StatusConflict {
		t.Fatalf("attempt reference payload collision should conflict: %d", code)
	}
	if code, body := paymentCallback(a, admin, order.Data.OrderID, "attempt-late-failure", "failed", order.Data.TotalAmount); code != http.StatusOK || body["alreadyProcessed"] != true {
		t.Fatalf("failure callback must not downgrade paid order: code=%d body=%#v", code, body)
	}
	var payment, reservation string
	var attempts, events int
	if err := a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=?", order.Data.OrderID).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT status FROM shop_inventory_reservations WHERE cart_id=?", order.Data.OrderID).Scan(&reservation); err != nil {
		t.Fatal(err)
	}
	_ = a.db.QueryRow("SELECT count(*) FROM shop_payment_attempts WHERE order_id=?", order.Data.OrderID).Scan(&attempts)
	_ = a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE id=?", "shop.paid:"+order.Data.OrderID).Scan(&events)
	if payment != "paid" || reservation != "consumed" || attempts != 1 || events != 1 {
		t.Fatalf("payment=%s reservation=%s attempts=%d paid-events=%d", payment, reservation, attempts, events)
	}
}

func TestShopPaymentFailureCanRetryAndExpiryWins(t *testing.T) {
	a := appForTest(t)
	space := token()
	admin := login(t, a, "admin@suite.test", space)
	var me struct{ Data Identity `json:"data"` }
	_ = json.Unmarshal(request(a, http.MethodGet, "/api/auth/me?site=shop", nil, admin...).Body.Bytes(), &me)
	first := createPaymentTestOrder(t, a, admin, me.Data.OrganizationID, me.Data.UserID)
	if code, _ := paymentCallback(a, admin, first.Data.OrderID, "fail-once", "failed", first.Data.TotalAmount); code != http.StatusOK {
		t.Fatalf("failed callback: %d", code)
	}
	if code, _ := paymentCallback(a, admin, first.Data.OrderID, "success-after-fail", "success", first.Data.TotalAmount); code != http.StatusOK {
		t.Fatalf("success retry: %d", code)
	}
	var status string
	_ = a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=?", first.Data.OrderID).Scan(&status)
	if status != "paid" {
		t.Fatalf("retry left payment status %s", status)
	}

	second := createPaymentTestOrder(t, a, admin, me.Data.OrganizationID, me.Data.UserID)
	if _, err := a.db.Exec("UPDATE shop_inventory_reservations SET expires_at=? WHERE cart_id=?", time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), second.Data.OrderID); err != nil {
		t.Fatal(err)
	}
	if code, _ := paymentCallback(a, admin, second.Data.OrderID, "too-late", "success", second.Data.TotalAmount); code != http.StatusConflict {
		t.Fatalf("expired callback should conflict: %d", code)
	}
	_ = a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=?", second.Data.OrderID).Scan(&status)
	var attempts int
	_ = a.db.QueryRow("SELECT count(*) FROM shop_payment_attempts WHERE order_id=?", second.Data.OrderID).Scan(&attempts)
	if status != "expired" || attempts != 0 {
		t.Fatalf("late callback mutated expired order: status=%s attempts=%d", status, attempts)
	}
}

func TestShopPaymentCallbacksSerializeAndEnforceTenantOwnerRole(t *testing.T) {
	a := appForTest(t)
	space := token()
	admin := login(t, a, "admin@suite.test", space)
	var me struct{ Data Identity `json:"data"` }
	_ = json.Unmarshal(request(a, http.MethodGet, "/api/auth/me?site=shop", nil, admin...).Body.Bytes(), &me)
	order := createPaymentTestOrder(t, a, admin, me.Data.OrganizationID, me.Data.UserID)
	if code, _ := paymentCallback(a, loginSite(t, a, "office@suite.test", space, "shop"), order.Data.OrderID, "not-owner", "success", order.Data.TotalAmount); code != http.StatusForbidden {
		t.Fatalf("non-owner operator should be forbidden: %d", code)
	}
	if code, _ := paymentCallback(a, loginSite(t, a, "viewer@suite.test", space, "shop"), order.Data.OrderID, "viewer", "success", order.Data.TotalAmount); code != http.StatusForbidden {
		t.Fatalf("readonly should be forbidden: %d", code)
	}
	if code, _ := paymentCallback(a, login(t, a, "admin@suite.test", token()), order.Data.OrderID, "foreign", "success", order.Data.TotalAmount); code != http.StatusNotFound {
		t.Fatalf("cross-tenant order should be hidden: %d", code)
	}
	otherTenant := login(t, a, "admin@suite.test", token())
	other := createPaymentTestOrder(t, a, otherTenant, "", "")
	_ = other

	const callbacks = 24
	var wg sync.WaitGroup
	statuses := make(chan int, callbacks)
	for i := 0; i < callbacks; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status := "failed"
			if i%2 == 0 {
				status = "success"
			}
			code, _ := paymentCallback(a, admin, order.Data.OrderID, fmt.Sprintf("race-%d", i), status, order.Data.TotalAmount)
			statuses <- code
		}(i)
	}
	wg.Wait()
	close(statuses)
	for code := range statuses {
		if code != http.StatusOK {
			t.Fatalf("callback race returned %d", code)
		}
	}
	var payment, reservation string
	var paidEvents int
	_ = a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE id=?", order.Data.OrderID).Scan(&payment)
	_ = a.db.QueryRow("SELECT status FROM shop_inventory_reservations WHERE cart_id=?", order.Data.OrderID).Scan(&reservation)
	_ = a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE id=?", "shop.paid:"+order.Data.OrderID).Scan(&paidEvents)
	if payment != "paid" || reservation != "consumed" || paidEvents != 1 {
		t.Fatalf("race invariant: payment=%s reservation=%s events=%d", payment, reservation, paidEvents)
	}
}
