package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestShopShipmentAuthorizationPaidOnlyAndIdempotency(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	operator := loginSite(t, a, "office@suite.test", space, "shop")
	viewer := loginSite(t, a, "viewer@suite.test", space, "shop")
	front := loginSite(t, a, "front@suite.test", space, "shop")
	otherTenant := loginSite(t, a, "admin@suite.test", token(), "shop")
	var customerID string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	seedOrder := func(name, payment, fulfillment string) string {
		t.Helper()
		id := "shipment-order-" + name + "-" + token()
		_, err := a.db.Exec(`INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,shipping_fee,total_amount,payment_status,fulfillment_status) VALUES(?,?,?,?,?,'{}',100,60,160,?,?)`, id, org, id, customerID, "front@suite.test", payment, fulfillment)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	requestShipment := func(orderID, tracking, carrier, key string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		input := map[string]any{"orderId": orderID, "trackingNumber": tracking, "carrier": carrier, "idempotencyKey": key}
		return request(a, http.MethodPost, "/api/stations/shop/ship", input, cookies...)
	}

	paid := seedOrder("paid", "paid", "unfulfilled")
	if w := requestShipment(paid, "TRACK-001", "Demo carrier", "ship-validation-01", operator...); w.Code != http.StatusOK {
		t.Fatalf("back-office operator shipment: %d %s", w.Code, w.Body.String())
	}
	var replay struct {
		Data struct {
			TrackingNumber string `json:"trackingNumber"`
			Duplicate      bool   `json:"duplicate"`
		} `json:"data"`
	}
	w := requestShipment(paid, "TRACK-001", "Demo carrier", "ship-validation-01", operator...)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &replay) != nil || !replay.Data.Duplicate || replay.Data.TrackingNumber != "TRACK-001" {
		t.Fatalf("exact retry: %d %s", w.Code, w.Body.String())
	}
	if w := requestShipment(paid, "TRACK-001", "Demo carrier", "ship-validation-01", admin...); w.Code != http.StatusConflict {
		t.Fatalf("retry key replayed by another actor: %d %s", w.Code, w.Body.String())
	}
	if w := requestShipment(paid, "TRACK-CHANGED", "Demo carrier", "ship-validation-01", operator...); w.Code != http.StatusConflict {
		t.Fatalf("idempotency key reused with changed tracking: %d %s", w.Code, w.Body.String())
	}
	if w := requestShipment(paid, "TRACK-OTHER", "Other carrier", "ship-second-key-01", admin...); w.Code != http.StatusConflict {
		t.Fatalf("already shipped order: %d %s", w.Code, w.Body.String())
	}
	var fulfillment, tracking string
	var shipmentCount, auditCount, idempotencyCount int
	if err := a.db.QueryRow("SELECT fulfillment_status,tracking_number FROM shop_orders WHERE organization_id=? AND id=?", org, paid).Scan(&fulfillment, &tracking); err != nil || fulfillment != "shipped" || tracking != "TRACK-001" {
		t.Fatalf("order after shipment: state=%s tracking=%s err=%v", fulfillment, tracking, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", org, paid).Scan(&shipmentCount); err != nil || shipmentCount != 1 {
		t.Fatalf("shipment rows=%d err=%v", shipmentCount, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action='shop.shipment.create'", org, paid).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("shipment audit rows=%d err=%v", auditCount, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND key='ship-validation-01' AND endpoint='shop_shipment'", org).Scan(&idempotencyCount); err != nil || idempotencyCount != 1 {
		t.Fatalf("shipment idempotency rows=%d err=%v", idempotencyCount, err)
	}

	unpaid := seedOrder("unpaid", "pending", "unfulfilled")
	refunded := seedOrder("refunded", "refunded", "unfulfilled")
	for _, id := range []string{unpaid, refunded} {
		if w := requestShipment(id, "TRACK-NO", "Demo carrier", "ship-not-paid-01", admin...); w.Code != http.StatusConflict {
			t.Errorf("non-paid order %s: %d %s", id, w.Code, w.Body.String())
		}
		if err := a.db.QueryRow("SELECT fulfillment_status FROM shop_orders WHERE organization_id=? AND id=?", org, id).Scan(&fulfillment); err != nil || fulfillment != "unfulfilled" {
			t.Errorf("non-paid order changed: state=%s err=%v", fulfillment, err)
		}
		if err := a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", org, id).Scan(&shipmentCount); err != nil || shipmentCount != 0 {
			t.Errorf("non-paid shipment rows=%d err=%v", shipmentCount, err)
		}
	}
	if w := requestShipment(unpaid, "TRACK-NO", "Demo carrier", "ship-readonly-01", viewer...); w.Code != http.StatusForbidden {
		t.Fatalf("readonly shipment: %d %s", w.Code, w.Body.String())
	}
	if w := requestShipment(unpaid, "TRACK-NO", "Demo carrier", "ship-front-0001", front...); w.Code != http.StatusForbidden {
		t.Fatalf("front-office shipment: %d %s", w.Code, w.Body.String())
	}
	if w := requestShipment(paid, "TRACK-FOREIGN", "Demo carrier", "ship-foreign-01", otherTenant...); w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant order: %d %s", w.Code, w.Body.String())
	}
	if w := requestShipment(unpaid, "", "Demo carrier", "ship-invalid-001", admin...); w.Code != http.StatusBadRequest {
		t.Fatalf("empty tracking: %d %s", w.Code, w.Body.String())
	}
}

func TestShopShipmentConcurrentRequestsCreateOneShipment(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	orderID := "shipment-race-" + token()
	if _, err := a.db.Exec(`INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,shipping_fee,total_amount,payment_status,fulfillment_status) VALUES(?,?,?,?,?,'{}',100,60,160,'paid','unfulfilled')`, orderID, org, orderID, owner, "front@suite.test"); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wg sync.WaitGroup
	codes := make(chan int, workers)
	for n := 0; n < workers; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			input := map[string]any{"orderId": orderID, "trackingNumber": "TRACK-RACE", "carrier": "Demo carrier", "idempotencyKey": fmt.Sprintf("ship-race-%05d", n)}
			codes <- request(a, http.MethodPost, "/api/stations/shop/ship", input, admin...).Code
		}(n)
	}
	wg.Wait()
	close(codes)
	successes, conflicts := 0, 0
	for code := range codes {
		switch code {
		case http.StatusOK:
			successes++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected concurrent shipment status %d", code)
		}
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("shipment race returned %d successes and %d conflicts", successes, conflicts)
	}
	var shipmentCount, auditCount, idempotencyCount int
	if err := a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", org, orderID).Scan(&shipmentCount); err != nil || shipmentCount != 1 {
		t.Fatalf("shipment race rows=%d err=%v", shipmentCount, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action='shop.shipment.create'", org, orderID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("shipment race audit rows=%d err=%v", auditCount, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='shop_shipment' AND key LIKE 'ship-race-%'", org).Scan(&idempotencyCount); err != nil || idempotencyCount != 1 {
		t.Fatalf("shipment race idempotency rows=%d err=%v", idempotencyCount, err)
	}
}
