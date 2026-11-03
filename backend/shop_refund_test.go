package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type refundTestOrder struct {
	orderID   string
	itemID    string
	variantID string
}

func seedRefundTestOrder(t *testing.T, a *App, organizationID, userID, name, paymentStatus, fulfillmentStatus string, quantity, lineTotal, stock int) refundTestOrder {
	t.Helper()
	suffix := token()
	productID, variantID := "refund-product-"+suffix, "refund-variant-"+suffix
	orderID, itemID := "refund-order-"+name+"-"+suffix, "refund-item-"+suffix
	if _, err := a.db.Exec("INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", productID, organizationID, "Refund test product", suffix, "fixture", "refund-category"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,?,0,1)", variantID, organizationID, productID, "SKU-"+suffix, "Standard", 334, stock); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,shipping_fee,total_amount,payment_status,fulfillment_status,tracking_number) VALUES(?,?,?,?,?,'{}',?,60,?,?,?,?)`, orderID, organizationID, orderID, userID, "customer@suite.test", lineTotal, lineTotal+60, paymentStatus, fulfillmentStatus, "TRACK-KEEP"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,?,?)", itemID, organizationID, orderID, variantID, "Refund test product", "Standard", "SKU-"+suffix, 334, quantity, lineTotal); err != nil {
		t.Fatal(err)
	}
	return refundTestOrder{orderID: orderID, itemID: itemID, variantID: variantID}
}

func refundRequest(a *App, orderID, itemID string, quantity int, reason, key string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	return request(a, http.MethodPost, "/api/stations/shop/refund", map[string]any{"orderId": orderID, "orderItemId": itemID, "quantity": quantity, "reason": reason, "idempotencyKey": key}, cookies...)
}

func TestShopRefundAdminTenantPaymentBoundsAndPartialTotals(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	operator := loginSite(t, a, "office@suite.test", space, "shop")
	readonly := loginSite(t, a, "viewer@suite.test", space, "shop")
	front := loginSite(t, a, "front@suite.test", space, "shop")
	foreign := loginSite(t, a, "admin@suite.test", token(), "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	paid := seedRefundTestOrder(t, a, org, owner, "paid", "paid", "shipped", 3, 1000, 5)
	pending := seedRefundTestOrder(t, a, org, owner, "pending", "pending", "unfulfilled", 1, 500, 2)
	refunded := seedRefundTestOrder(t, a, org, owner, "refunded", "refunded", "shipped", 1, 500, 2)
	otherItem := seedRefundTestOrder(t, a, org, owner, "other", "paid", "unfulfilled", 1, 500, 3)

	for _, test := range []struct {
		name    string
		cookies []*http.Cookie
		want    int
	}{{"operator", operator, http.StatusForbidden}, {"readonly", readonly, http.StatusForbidden}, {"front", front, http.StatusForbidden}, {"foreign tenant", foreign, http.StatusNotFound}} {
		t.Run(test.name, func(t *testing.T) {
			w := refundRequest(a, paid.orderID, paid.itemID, 1, "Returned", "refund-role-0001", test.cookies...)
			if w.Code != test.want {
				t.Fatalf("status=%d want=%d response=%s", w.Code, test.want, w.Body.String())
			}
		})
	}
	if w := refundRequest(a, paid.orderID, otherItem.itemID, 1, "Wrong order item", "refund-wrong-item-01", admin...); w.Code != http.StatusConflict {
		t.Fatalf("item from another order: %d %s", w.Code, w.Body.String())
	}
	if w := refundRequest(a, pending.orderID, pending.itemID, 1, "Pending order", "refund-pending-001", admin...); w.Code != http.StatusConflict {
		t.Fatalf("pending order: %d %s", w.Code, w.Body.String())
	}
	if w := refundRequest(a, refunded.orderID, refunded.itemID, 1, "Refunded order", "refund-refunded-01", admin...); w.Code != http.StatusConflict {
		t.Fatalf("refunded order: %d %s", w.Code, w.Body.String())
	}
	if w := refundRequest(a, paid.orderID, paid.itemID, 0, "Invalid quantity", "refund-invalid-0001", admin...); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid quantity: %d %s", w.Code, w.Body.String())
	}

	type responseData struct {
		RefundAmount         int64  `json:"refundAmount"`
		ItemRefundedAmount   int64  `json:"itemRefundedAmount"`
		ItemRefundedQuantity int64  `json:"itemRefundedQuantity"`
		OrderRefundedAmount  int64  `json:"orderRefundedAmount"`
		PaymentStatus        string `json:"paymentStatus"`
		FulfillmentStatus    string `json:"fulfillmentStatus"`
		Duplicate            bool   `json:"duplicate"`
	}
	decode := func(w *httptest.ResponseRecorder) responseData {
		t.Helper()
		var envelope struct {
			Data responseData `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	first := refundRequest(a, paid.orderID, paid.itemID, 1, "Damaged stitching", "refund-partial-0001", admin...)
	if first.Code != http.StatusOK {
		t.Fatalf("first partial refund: %d %s", first.Code, first.Body.String())
	}
	firstResult := decode(first)
	if firstResult.RefundAmount != 333 || firstResult.ItemRefundedAmount != 333 || firstResult.OrderRefundedAmount != 333 || firstResult.PaymentStatus != "paid" || firstResult.FulfillmentStatus != "shipped" {
		t.Fatalf("first partial refund result: %+v", firstResult)
	}
	stock := func(variantID string) int {
		t.Helper()
		var value int
		if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", org, variantID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if stock(paid.variantID) != 6 {
		t.Fatalf("stock after first refund=%d", stock(paid.variantID))
	}
	replay := refundRequest(a, paid.orderID, paid.itemID, 1, "Damaged stitching", "refund-partial-0001", admin...)
	if replay.Code != http.StatusOK || !decode(replay).Duplicate || stock(paid.variantID) != 6 {
		t.Fatalf("refund replay: %d %s stock=%d", replay.Code, replay.Body.String(), stock(paid.variantID))
	}
	if w := refundRequest(a, paid.orderID, paid.itemID, 1, "Changed reason", "refund-partial-0001", admin...); w.Code != http.StatusConflict {
		t.Fatalf("changed retry payload: %d %s", w.Code, w.Body.String())
	}
	if w := refundRequest(a, paid.orderID, paid.itemID, 3, "Too many units", "refund-over-limit-01", admin...); w.Code != http.StatusConflict {
		t.Fatalf("over-return: %d %s", w.Code, w.Body.String())
	}
	second := refundRequest(a, paid.orderID, paid.itemID, 1, "Size exchange", "refund-partial-0002", admin...)
	third := refundRequest(a, paid.orderID, paid.itemID, 1, "Final unit", "refund-partial-0003", admin...)
	if second.Code != http.StatusOK || decode(second).RefundAmount != 333 || third.Code != http.StatusOK || decode(third).RefundAmount != 334 {
		t.Fatalf("cumulative rounding: second=%d %s third=%d %s", second.Code, second.Body.String(), third.Code, third.Body.String())
	}
	var payment, fulfillment, tracking string
	if err := a.db.QueryRow("SELECT payment_status,fulfillment_status,tracking_number FROM shop_orders WHERE organization_id=? AND id=?", org, paid.orderID).Scan(&payment, &fulfillment, &tracking); err != nil || payment != "paid" || fulfillment != "shipped" || tracking != "TRACK-KEEP" {
		t.Fatalf("order state changed by refund: payment=%s fulfillment=%s tracking=%s err=%v", payment, fulfillment, tracking, err)
	}
	if stock(paid.variantID) != 8 {
		t.Fatalf("stock after full item refund=%d", stock(paid.variantID))
	}
	var returns, audit, idempotency int
	if err := a.db.QueryRow("SELECT count(*) FROM shop_returns WHERE organization_id=? AND order_id=?", org, paid.orderID).Scan(&returns); err != nil || returns != 3 {
		t.Fatalf("refund rows=%d err=%v", returns, err)
	}
	var returnedQty, returnedAmount int64
	if err := a.db.QueryRow("SELECT COALESCE(sum(quantity),0),COALESCE(sum(refund_amount),0) FROM shop_returns WHERE organization_id=? AND order_id=? AND status='refunded'", org, paid.orderID).Scan(&returnedQty, &returnedAmount); err != nil || returnedQty != 3 || returnedAmount != 1000 {
		t.Fatalf("cumulative product refund qty=%d amount=%d err=%v", returnedQty, returnedAmount, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action='shop.refund.create'", org, paid.orderID).Scan(&audit); err != nil || audit != 3 {
		t.Fatalf("refund audit rows=%d err=%v", audit, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='shop_refund' AND key LIKE 'refund-partial-%'", org).Scan(&idempotency); err != nil || idempotency != 3 {
		t.Fatalf("refund idempotency rows=%d err=%v", idempotency, err)
	}

	legacyReturn := seedRefundTestOrder(t, a, org, owner, "legacy-return", "paid", "shipped", 2, 500, 0)
	if _, err := a.db.Exec("INSERT INTO shop_returns(id,organization_id,order_id,order_item_id,quantity,reason,status,refund_amount) VALUES(?,?,?,?,1,'Legacy request','requested',0)", "legacy-return-"+token(), org, legacyReturn.orderID, legacyReturn.itemID); err != nil {
		t.Fatal(err)
	}
	if w := refundRequest(a, legacyReturn.orderID, legacyReturn.itemID, 2, "Would exceed legacy return", "refund-legacy-over-01", admin...); w.Code != http.StatusConflict {
		t.Fatalf("legacy requested quantity was not counted: %d %s", w.Code, w.Body.String())
	}
	legacyRefund := refundRequest(a, legacyReturn.orderID, legacyReturn.itemID, 1, "Return remaining unit", "refund-legacy-valid-01", admin...)
	if legacyRefund.Code != http.StatusOK || decode(legacyRefund).RefundAmount != 250 || stock(legacyReturn.variantID) != 1 {
		t.Fatalf("legacy quantity reconciliation: %d %s stock=%d", legacyRefund.Code, legacyRefund.Body.String(), stock(legacyReturn.variantID))
	}
}

func TestShopConcurrentRefundsCannotOverReturnOrRestockTwice(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	seed := seedRefundTestOrder(t, a, org, owner, "race", "paid", "unfulfilled", 2, 501, 0)
	const workers = 12
	var wg sync.WaitGroup
	codes := make(chan int, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("refund-race-%05d", i)
			codes <- refundRequest(a, seed.orderID, seed.itemID, 2, "Concurrent return", key, admin...).Code
		}(i)
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
			t.Errorf("unexpected concurrent refund status %d", code)
		}
	}
	if successes != 1 || conflicts != workers-1 {
		t.Fatalf("refund race returned %d successes and %d conflicts", successes, conflicts)
	}
	var returnedQty, refundRows, stock int
	if err := a.db.QueryRow("SELECT COALESCE(sum(quantity),0),count(*) FROM shop_returns WHERE organization_id=? AND order_id=?", org, seed.orderID).Scan(&returnedQty, &refundRows); err != nil || returnedQty != 2 || refundRows != 1 {
		t.Fatalf("refund race quantity=%d rows=%d err=%v", returnedQty, refundRows, err)
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", org, seed.variantID).Scan(&stock); err != nil || stock != 2 {
		t.Fatalf("refund race stock=%d err=%v", stock, err)
	}
	var audit, idempotency int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action='shop.refund.create'", org, seed.orderID).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("refund race audit=%d err=%v", audit, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='shop_refund' AND key LIKE 'refund-race-%'", org).Scan(&idempotency); err != nil || idempotency != 1 {
		t.Fatalf("refund race idempotency=%d err=%v", idempotency, err)
	}
}

func TestShopRefundRollsBackWhenAuditWriteFails(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	seed := seedRefundTestOrder(t, a, org, owner, "rollback", "paid", "shipped", 1, 500, 0)
	if _, err := a.db.Exec("CREATE TRIGGER fail_shop_refund_audit BEFORE INSERT ON audit_events WHEN NEW.action='shop.refund.create' BEGIN SELECT RAISE(ABORT,'audit rejected'); END"); err != nil {
		t.Fatal(err)
	}
	if w := refundRequest(a, seed.orderID, seed.itemID, 1, "Rollback check", "refund-rollback-001", admin...); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("audit fault response: %d %s", w.Code, w.Body.String())
	}
	var returns, retries, audits, stock int
	if err := a.db.QueryRow("SELECT count(*) FROM shop_returns WHERE organization_id=? AND order_id=?", org, seed.orderID).Scan(&returns); err != nil || returns != 0 {
		t.Fatalf("rollback left returns=%d err=%v", returns, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM idempotency_keys WHERE organization_id=? AND endpoint='shop_refund' AND key='refund-rollback-001'", org).Scan(&retries); err != nil || retries != 0 {
		t.Fatalf("rollback left retry rows=%d err=%v", retries, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action='shop.refund.create'", org, seed.orderID).Scan(&audits); err != nil || audits != 0 {
		t.Fatalf("rollback left audit rows=%d err=%v", audits, err)
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", org, seed.variantID).Scan(&stock); err != nil || stock != 0 {
		t.Fatalf("rollback left stock=%d err=%v", stock, err)
	}
}

func TestShopFullMerchandiseRefundCancelsUnshippedOrderAndBlocksShipment(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	seed := seedRefundTestOrder(t, a, org, owner, "full-cancel", "paid", "unfulfilled", 2, 1000, 0)
	w := refundRequest(a, seed.orderID, seed.itemID, 2, "Cancel unshipped order", "refund-full-cancel-01", admin...)
	if w.Code != http.StatusOK {
		t.Fatalf("full merchandise refund: %d %s", w.Code, w.Body.String())
	}
	var refund struct {
		Data struct {
			RefundAmount      int64  `json:"refundAmount"`
			PaymentStatus     string `json:"paymentStatus"`
			FulfillmentStatus string `json:"fulfillmentStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &refund); err != nil || refund.Data.RefundAmount != 1000 || refund.Data.PaymentStatus != "paid" || refund.Data.FulfillmentStatus != "cancelled" {
		t.Fatalf("full return response=%+v err=%v", refund.Data, err)
	}
	var payment, fulfillment string
	if err := a.db.QueryRow("SELECT payment_status,fulfillment_status FROM shop_orders WHERE organization_id=? AND id=?", org, seed.orderID).Scan(&payment, &fulfillment); err != nil || payment != "paid" || fulfillment != "cancelled" {
		t.Fatalf("order state payment=%s fulfillment=%s err=%v", payment, fulfillment, err)
	}
	var subtotal, shippingFee, totalAmount int
	if err := a.db.QueryRow("SELECT subtotal,shipping_fee,total_amount FROM shop_orders WHERE organization_id=? AND id=?", org, seed.orderID).Scan(&subtotal, &shippingFee, &totalAmount); err != nil || subtotal != 1000 || shippingFee != 60 || totalAmount != 1060 {
		t.Fatalf("merchandise refund boundary subtotal=%d shipping=%d total=%d err=%v", subtotal, shippingFee, totalAmount, err)
	}
	ship := request(a, http.MethodPost, "/api/stations/shop/ship", map[string]any{"orderId": seed.orderID, "trackingNumber": "TRACK-BLOCKED", "carrier": "Demo carrier", "idempotencyKey": "ship-after-full-01"}, admin...)
	if ship.Code != http.StatusConflict {
		t.Fatalf("shipment after full merchandise refund: %d %s", ship.Code, ship.Body.String())
	}
	var shipmentCount int
	if err := a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", org, seed.orderID).Scan(&shipmentCount); err != nil || shipmentCount != 0 {
		t.Fatalf("shipment after full refund rows=%d err=%v", shipmentCount, err)
	}
}

func TestShopPartialRefundHoldsUnshippedOrderAndShipmentRejectsIt(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	seed := seedRefundTestOrder(t, a, org, owner, "partial-hold", "paid", "unfulfilled", 3, 1000, 0)
	refund := refundRequest(a, seed.orderID, seed.itemID, 1, "Partial return", "refund-partial-hold-01", admin...)
	if refund.Code != http.StatusOK {
		t.Fatalf("partial refund: %d %s", refund.Code, refund.Body.String())
	}
	var result struct {
		Data struct {
			RefundAmount      int64  `json:"refundAmount"`
			PaymentStatus     string `json:"paymentStatus"`
			FulfillmentStatus string `json:"fulfillmentStatus"`
		} `json:"data"`
	}
	if err := json.Unmarshal(refund.Body.Bytes(), &result); err != nil || result.Data.RefundAmount != 333 || result.Data.PaymentStatus != "paid" || result.Data.FulfillmentStatus != "return_hold" {
		t.Fatalf("partial return state=%+v err=%v", result.Data, err)
	}
	ship := request(a, http.MethodPost, "/api/stations/shop/ship", map[string]any{"orderId": seed.orderID, "trackingNumber": "TRACK-HOLD", "carrier": "Demo carrier", "idempotencyKey": "ship-return-hold-01"}, admin...)
	if ship.Code != http.StatusConflict {
		t.Fatalf("shipment after partial return: %d %s", ship.Code, ship.Body.String())
	}
	var shipmentCount int
	if err := a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", org, seed.orderID).Scan(&shipmentCount); err != nil || shipmentCount != 0 {
		t.Fatalf("shipment on return hold rows=%d err=%v", shipmentCount, err)
	}
}

func TestShopShipmentRejectsLegacyReturnRowsWithoutUpdatedFulfillmentState(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	seed := seedRefundTestOrder(t, a, org, owner, "legacy-partial", "paid", "unfulfilled", 2, 500, 0)
	if _, err := a.db.Exec("INSERT INTO shop_returns(id,organization_id,order_id,order_item_id,quantity,reason,status,refund_amount) VALUES(?,?,?,?,1,'Legacy partial return','refunded',250)", "legacy-return-"+token(), org, seed.orderID, seed.itemID); err != nil {
		t.Fatal(err)
	}
	ship := request(a, http.MethodPost, "/api/stations/shop/ship", map[string]any{"orderId": seed.orderID, "trackingNumber": "TRACK-LEGACY", "carrier": "Demo carrier", "idempotencyKey": "ship-legacy-return-01"}, admin...)
	if ship.Code != http.StatusConflict {
		t.Fatalf("shipment with legacy return row: %d %s", ship.Code, ship.Body.String())
	}
	var shipmentCount int
	if err := a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", org, seed.orderID).Scan(&shipmentCount); err != nil || shipmentCount != 0 {
		t.Fatalf("legacy partial return shipment rows=%d err=%v", shipmentCount, err)
	}
}

func TestShopPartialRefundRacingShipmentHasSafeSerializedOutcome(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var owner string
	if err := a.db.QueryRow("SELECT user_id FROM station_accounts WHERE organization_id=? AND email='front@suite.test'", org).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	seed := seedRefundTestOrder(t, a, org, owner, "refund-ship-race", "paid", "unfulfilled", 2, 1000, 0)
	type outcome struct {
		kind string
		code int
	}
	results := make(chan outcome, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		results <- outcome{kind: "refund", code: refundRequest(a, seed.orderID, seed.itemID, 1, "Concurrent partial return", "refund-ship-race-01", admin...).Code}
	}()
	go func() {
		defer wg.Done()
		response := request(a, http.MethodPost, "/api/stations/shop/ship", map[string]any{"orderId": seed.orderID, "trackingNumber": "TRACK-RACE", "carrier": "Demo carrier", "idempotencyKey": "ship-refund-race-01"}, admin...)
		results <- outcome{kind: "shipment", code: response.Code}
	}()
	wg.Wait()
	close(results)
	refundCode, shipmentCode := 0, 0
	for result := range results {
		if result.kind == "refund" {
			refundCode = result.code
		} else {
			shipmentCode = result.code
		}
	}
	if refundCode != http.StatusOK || shipmentCode != http.StatusOK && shipmentCode != http.StatusConflict {
		t.Fatalf("refund/shipment race: refund=%d shipment=%d", refundCode, shipmentCode)
	}
	var payment, fulfillment string
	var shipmentCount, returnCount, stock int
	if err := a.db.QueryRow("SELECT payment_status,fulfillment_status FROM shop_orders WHERE organization_id=? AND id=?", org, seed.orderID).Scan(&payment, &fulfillment); err != nil || payment != "paid" {
		t.Fatalf("race order payment=%s fulfillment=%s err=%v", payment, fulfillment, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", org, seed.orderID).Scan(&shipmentCount); err != nil || shipmentCount != 1 && shipmentCount != 0 {
		t.Fatalf("race shipment rows=%d err=%v", shipmentCount, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM shop_returns WHERE organization_id=? AND order_id=?", org, seed.orderID).Scan(&returnCount); err != nil || returnCount != 1 {
		t.Fatalf("race return rows=%d err=%v", returnCount, err)
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", org, seed.variantID).Scan(&stock); err != nil || stock != 1 {
		t.Fatalf("race stock=%d err=%v", stock, err)
	}
	if shipmentCode == http.StatusOK && (fulfillment != "shipped" || shipmentCount != 1) {
		t.Fatalf("shipment won before return but state is %s with %d shipment", fulfillment, shipmentCount)
	}
	if shipmentCode == http.StatusConflict && (fulfillment != "return_hold" || shipmentCount != 0) {
		t.Fatalf("return won before shipment but state is %s with %d shipment", fulfillment, shipmentCount)
	}
}
