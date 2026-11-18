package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

type couponTestCatalog struct {
	orgID    string
	userID   string
	space    string
	admin    []*http.Cookie
	variantA string
	variantB string
}

func seedCouponTestCatalog(t *testing.T, a *App) couponTestCatalog {
	t.Helper()
	space := token()
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	var me struct {
		Data Identity `json:"data"`
	}
	if w := request(a, http.MethodGet, "/api/auth/me?site=shop", nil, admin...); w.Code != http.StatusOK {
		t.Fatalf("shop identity: %d %s", w.Code, w.Body.String())
	} else if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	org := me.Data.OrganizationID
	category := "coupon-category-" + token()
	if _, err := a.db.Exec("INSERT INTO shop_categories(id,organization_id,name,slug) VALUES(?,?,?,?)", token(), org, "Coupon test", category); err != nil {
		t.Fatal(err)
	}
	product := "coupon-product-" + token()
	if _, err := a.db.Exec("INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", product, org, "Coupon product", product, "test", category); err != nil {
		t.Fatal(err)
	}
	variants := []string{"coupon-variant-" + token(), "coupon-variant-" + token()}
	for i, variant := range variants {
		price := 100
		if i == 1 {
			price = 100
		}
		if _, err := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,20,0,1)", variant, org, product, "SKU-"+variant, "Variant", price); err != nil {
			t.Fatal(err)
		}
	}
	return couponTestCatalog{orgID: org, userID: me.Data.UserID, space: space, admin: admin, variantA: variants[0], variantB: variants[1]}
}

func addCouponTestPromotion(t *testing.T, a *App, org, code, kind string, value, minSpend int, expires any, active int) {
	t.Helper()
	if _, err := a.db.Exec("INSERT INTO shop_promotions(id,organization_id,code,description,discount_type,discount_value,min_spend,expires_at,is_active) VALUES(?,?,?,?,?,?,?,?,?)", "coupon-promo-"+token(), org, code, "coupon test", kind, value, minSpend, expires, active); err != nil {
		t.Fatal(err)
	}
}

func couponCheckout(a *App, cookies []*http.Cookie, key, code string, items ...map[string]any) *httptest.ResponseRecorder {
	return request(a, http.MethodPost, "/api/stations/shop/checkout", map[string]any{
		"items": items, "couponCode": code, "idempotencyKey": key,
		"customerEmail":   "buyer@example.test",
		"shippingAddress": map[string]string{"name": "Buyer", "city": "Taipei", "address": "Demo", "phone": "0912345678"},
	}, cookies...)
}

func TestShopCheckoutReplaysLegacySingleItemIdempotencyFingerprint(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	orderID, orderNo, key := "legacy-order-"+token(), "LEGACY-"+token()[:12], "legacy-checkout-key"
	address := map[string]any{"name": "Buyer", "city": "Taipei", "address": "Demo", "phone": "0912345678"}
	addressBytes, err := json.Marshal(address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,discount_amount,shipping_fee,total_amount,payment_status,fulfillment_status,idempotency_key) VALUES(?,?,?,?,?,?,100,0,60,160,'pending','unfulfilled',?)`, orderID, catalog.orgID, orderNo, catalog.userID, "buyer@example.test", string(addressBytes), key); err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,1,100)", "legacy-item-"+token(), catalog.orgID, orderID, catalog.variantA, "Coupon product", "Variant", "legacy-sku", 100); err != nil {
		t.Fatal(err)
	}
	legacyHash := digest(catalog.userID + "\n" + catalog.variantA + "\n1\n" + "buyer@example.test" + "\n" + string(addressBytes))
	legacyBody, err := json.Marshal(map[string]any{"orderId": orderID, "orderNumber": orderNo, "subtotal": 100, "shippingFee": 60, "totalAmount": 160, "unitPrice": 100, "quantity": 1, "paymentStatus": "pending"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,'shop_checkout',?,200,?)", "legacy-idem-"+token(), catalog.orgID, key, legacyHash, string(legacyBody)); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"variantId": catalog.variantA, "quantity": 1, "idempotencyKey": key, "customerEmail": "buyer@example.test", "shippingAddress": address}
	w := request(a, http.MethodPost, "/api/stations/shop/checkout", payload, catalog.admin...)
	if w.Code != http.StatusOK {
		t.Fatalf("legacy exact replay: %d %s", w.Code, w.Body.String())
	}
	var replay struct {
		Data struct {
			OrderID   string `json:"orderId"`
			Duplicate bool   `json:"duplicate"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &replay); err != nil || replay.Data.OrderID != orderID || !replay.Data.Duplicate {
		t.Fatalf("legacy replay=%+v err=%v", replay.Data, err)
	}
	changed := map[string]any{"variantId": catalog.variantA, "quantity": 2, "idempotencyKey": key, "customerEmail": "buyer@example.test", "shippingAddress": address}
	if w = request(a, http.MethodPost, "/api/stations/shop/checkout", changed, catalog.admin...); w.Code != http.StatusConflict {
		t.Fatalf("legacy key with changed quantity: %d %s", w.Code, w.Body.String())
	}
	withCoupon := map[string]any{"variantId": catalog.variantA, "quantity": 1, "couponCode": "SAVE10", "idempotencyKey": key, "customerEmail": "buyer@example.test", "shippingAddress": address}
	if w = request(a, http.MethodPost, "/api/stations/shop/checkout", withCoupon, catalog.admin...); w.Code != http.StatusConflict {
		t.Fatalf("legacy key with added coupon: %d %s", w.Code, w.Body.String())
	}
	otherUser := loginSite(t, a, "office@suite.test", catalog.space, "shop")
	if w = request(a, http.MethodPost, "/api/stations/shop/checkout", payload, otherUser...); w.Code != http.StatusConflict {
		t.Fatalf("legacy key with different actor: %d %s", w.Code, w.Body.String())
	}
}

func TestShopCouponCheckoutPricesOnServerAndRefundsDiscountedLineAmounts(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	addCouponTestPromotion(t, a, catalog.orgID, "SAVE10", "percentage", 10, 100, nil, 1)
	w := couponCheckout(a, catalog.admin, "coupon-checkout-001", "SAVE10", map[string]any{"variantId": catalog.variantA, "quantity": 2}, map[string]any{"variantId": catalog.variantB, "quantity": 1})
	if w.Code != http.StatusOK {
		t.Fatalf("coupon checkout: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			OrderID    string `json:"orderId"`
			Subtotal   int    `json:"subtotal"`
			Discount   int    `json:"discountAmount"`
			Shipping   int    `json:"shippingFee"`
			Total      int    `json:"totalAmount"`
			CouponCode string `json:"couponCode"`
			Duplicate  bool   `json:"duplicate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Subtotal != 300 || envelope.Data.Discount != 30 || envelope.Data.Shipping != 60 || envelope.Data.Total != 330 || envelope.Data.CouponCode != "SAVE10" || envelope.Data.OrderID == "" {
		t.Fatalf("legacy 10%% coupon price sequence mismatch: %+v", envelope.Data)
	}
	var lineA, lineB int
	if err := a.db.QueryRow("SELECT line_total FROM shop_order_items WHERE organization_id=? AND order_id=? AND variant_id=?", catalog.orgID, envelope.Data.OrderID, catalog.variantA).Scan(&lineA); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT line_total FROM shop_order_items WHERE organization_id=? AND order_id=? AND variant_id=?", catalog.orgID, envelope.Data.OrderID, catalog.variantB).Scan(&lineB); err != nil {
		t.Fatal(err)
	}
	if lineA != 180 || lineB != 90 || lineA+lineB != envelope.Data.Subtotal-envelope.Data.Discount {
		t.Fatalf("discount allocation lineA=%d lineB=%d", lineA, lineB)
	}
	// Refunds use each persisted, discounted item line total, including exact split rounding.
	if _, err := a.db.Exec("UPDATE shop_orders SET payment_status='paid' WHERE organization_id=? AND id=?", catalog.orgID, envelope.Data.OrderID); err != nil {
		t.Fatal(err)
	}
	var itemA string
	if err := a.db.QueryRow("SELECT id FROM shop_order_items WHERE organization_id=? AND order_id=? AND variant_id=?", catalog.orgID, envelope.Data.OrderID, catalog.variantA).Scan(&itemA); err != nil {
		t.Fatal(err)
	}
	first := refundRequest(a, envelope.Data.OrderID, itemA, 1, "Coupon order return", "coupon-refund-001", catalog.admin...)
	second := refundRequest(a, envelope.Data.OrderID, itemA, 1, "Coupon order return", "coupon-refund-002", catalog.admin...)
	var firstResult, secondResult struct {
		Data struct {
			RefundAmount int64 `json:"refundAmount"`
		} `json:"data"`
	}
	if first.Code != http.StatusOK || json.Unmarshal(first.Body.Bytes(), &firstResult) != nil || firstResult.Data.RefundAmount != 90 {
		t.Fatalf("first discounted unit refund: %d %s result=%+v", first.Code, first.Body.String(), firstResult)
	}
	if second.Code != http.StatusOK || json.Unmarshal(second.Body.Bytes(), &secondResult) != nil || secondResult.Data.RefundAmount != 90 {
		t.Fatalf("second discounted unit refund: %d %s result=%+v", second.Code, second.Body.String(), secondResult)
	}
	if replay := couponCheckout(a, catalog.admin, "coupon-checkout-001", "SAVE10", map[string]any{"variantId": catalog.variantA, "quantity": 2}, map[string]any{"variantId": catalog.variantB, "quantity": 1}); replay.Code != http.StatusOK {
		t.Fatalf("exact coupon checkout retry: %d %s", replay.Code, replay.Body.String())
	} else {
		var retry struct {
			Data struct {
				OrderID   string `json:"orderId"`
				Coupon    string `json:"couponCode"`
				Duplicate bool   `json:"duplicate"`
			} `json:"data"`
		}
		if err := json.Unmarshal(replay.Body.Bytes(), &retry); err != nil || retry.Data.OrderID != envelope.Data.OrderID || retry.Data.Coupon != "SAVE10" || !retry.Data.Duplicate {
			t.Fatalf("coupon retry body=%s err=%v", replay.Body.String(), err)
		}
	}
	if changed := couponCheckout(a, catalog.admin, "coupon-checkout-001", "OTHER", map[string]any{"variantId": catalog.variantA, "quantity": 2}, map[string]any{"variantId": catalog.variantB, "quantity": 1}); changed.Code != http.StatusConflict {
		t.Fatalf("same key with different coupon must conflict: %d %s", changed.Code, changed.Body.String())
	}
}

func TestShopCouponBoundsExpiryTenantAndInvalidPromotions(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	foreign := seedCouponTestCatalog(t, a)
	addCouponTestPromotion(t, a, catalog.orgID, "MINIMUM", "fixed", 500, 200, nil, 1)
	addCouponTestPromotion(t, a, catalog.orgID, "EXPIRED", "fixed", 20, 0, "2000-01-01 00:00:00", 1)
	addCouponTestPromotion(t, a, catalog.orgID, "DISABLED", "percentage", 10, 0, nil, 0)
	addCouponTestPromotion(t, a, foreign.orgID, "FOREIGN", "fixed", 1, 0, nil, 1)
	for _, tc := range []struct {
		name string
		code string
		qty  int
	}{
		{"below minimum spend", "MINIMUM", 1},
		{"expired", "EXPIRED", 2},
		{"disabled", "DISABLED", 1},
		{"foreign tenant coupon", "FOREIGN", 1},
		{"unknown", "NOPE", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := couponCheckout(a, catalog.admin, "coupon-invalid-"+token(), tc.code, map[string]any{"variantId": catalog.variantA, "quantity": tc.qty})
			if w.Code != http.StatusConflict {
				t.Fatalf("invalid coupon status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
	addCouponTestPromotion(t, a, catalog.orgID, "CAP", "fixed", 500, 0, nil, 1)
	capOrder := couponCheckout(a, catalog.admin, "coupon-cap-0001", "CAP", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	if capOrder.Code != http.StatusOK {
		t.Fatalf("capped fixed coupon: %d %s", capOrder.Code, capOrder.Body.String())
	}
	var capData struct {
		Data struct {
			Discount int `json:"discountAmount"`
			Total    int `json:"totalAmount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(capOrder.Body.Bytes(), &capData); err != nil || capData.Data.Discount != 100 || capData.Data.Total != 60 {
		t.Fatalf("fixed coupon must cap at merchandise subtotal: %+v err=%v", capData.Data, err)
	}
	addCouponTestPromotion(t, a, catalog.orgID, "RATE101", "percentage", 101, 0, nil, 1)
	rateOrder := couponCheckout(a, catalog.admin, "coupon-ratecap-01", "RATE101", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	var rateData struct {
		Data struct {
			Discount int `json:"discountAmount"`
			Total    int `json:"totalAmount"`
		} `json:"data"`
	}
	if rateOrder.Code != http.StatusOK || json.Unmarshal(rateOrder.Body.Bytes(), &rateData) != nil || rateData.Data.Discount != 100 || rateData.Data.Total != 60 {
		t.Fatalf("percentage coupon must cap at merchandise subtotal: status=%d body=%s result=%+v", rateOrder.Code, rateOrder.Body.String(), rateData.Data)
	}
}

func TestShopCouponRemainderRefundsSumToDiscountedLineAmount(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	addCouponTestPromotion(t, a, catalog.orgID, "ODD1", "fixed", 1, 0, nil, 1)
	w := couponCheckout(a, catalog.admin, "coupon-odd-cent-01", "ODD1", map[string]any{"variantId": catalog.variantA, "quantity": 2})
	if w.Code != http.StatusOK {
		t.Fatalf("fixed one-unit coupon: %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var itemID string
	var discountedLine int
	if err := a.db.QueryRow("SELECT id,line_total FROM shop_order_items WHERE organization_id=? AND order_id=?", catalog.orgID, result.Data.OrderID).Scan(&itemID, &discountedLine); err != nil {
		t.Fatal(err)
	}
	if discountedLine != 199 {
		t.Fatalf("discounted line total=%d, want 199", discountedLine)
	}
	if _, err := a.db.Exec("UPDATE shop_orders SET payment_status='paid' WHERE organization_id=? AND id=?", catalog.orgID, result.Data.OrderID); err != nil {
		t.Fatal(err)
	}
	var amounts []int64
	for i, key := range []string{"coupon-odd-refund-1", "coupon-odd-refund-2"} {
		w := refundRequest(a, result.Data.OrderID, itemID, 1, "Return odd-priced discounted unit", key, catalog.admin...)
		var refund struct {
			Data struct {
				RefundAmount int64 `json:"refundAmount"`
			} `json:"data"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &refund) != nil {
			t.Fatalf("refund unit %d: %d %s", i, w.Code, w.Body.String())
		}
		amounts = append(amounts, refund.Data.RefundAmount)
	}
	if amounts[0] != 99 || amounts[1] != 100 || amounts[0]+amounts[1] != int64(discountedLine) {
		t.Fatalf("discounted refund split=%v, line total=%d", amounts, discountedLine)
	}
}

func TestShopCouponDiscountRemainderNeverMakesZeroPriceTailNegative(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	if _, err := a.db.Exec("UPDATE shop_variants SET price=1 WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantA); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE shop_variants SET price=2 WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantB); err != nil {
		t.Fatal(err)
	}
	var productID string
	if err := a.db.QueryRow("SELECT product_id FROM shop_variants WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantA).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	zeroVariant := "zz-zero-price-" + token()
	if _, err := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,0,5,0,1)", zeroVariant, catalog.orgID, productID, "SKU-ZERO-"+token(), "Free line"); err != nil {
		t.Fatal(err)
	}
	addCouponTestPromotion(t, a, catalog.orgID, "TWO-OFF-3", "fixed", 2, 0, nil, 1)
	addCouponTestPromotion(t, a, catalog.orgID, "FULL-OFF-3", "percentage", 100, 0, nil, 1)
	items := []map[string]any{{"variantId": catalog.variantA, "quantity": 1}, {"variantId": catalog.variantB, "quantity": 1}, {"variantId": zeroVariant, "quantity": 1}}
	for _, tc := range []struct {
		key, code string
		wantNet   int
	}{{"coupon-zero-tail-partial", "TWO-OFF-3", 1}, {"coupon-zero-tail-full", "FULL-OFF-3", 0}} {
		w := couponCheckout(a, catalog.admin, tc.key, tc.code, items...)
		if w.Code != http.StatusOK {
			t.Fatalf("zero-price tail %s checkout: %d %s", tc.code, w.Code, w.Body.String())
		}
		var result struct {
			Data struct {
				OrderID string `json:"orderId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		rows, err := a.db.Query("SELECT line_total FROM shop_order_items WHERE organization_id=? AND order_id=?", catalog.orgID, result.Data.OrderID)
		if err != nil {
			t.Fatal(err)
		}
		var sum int
		for rows.Next() {
			var lineTotal int
			if err = rows.Scan(&lineTotal); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			if lineTotal < 0 {
				rows.Close()
				t.Fatalf("coupon %s produced negative discounted line total %d", tc.code, lineTotal)
			}
			sum += lineTotal
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		if sum != tc.wantNet {
			t.Fatalf("coupon %s line total sum=%d want=%d", tc.code, sum, tc.wantNet)
		}
	}
}

func TestShopMultiItemPaymentConsumesAndExpiresAllReservationsAtomically(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	makeOrder := func(key string) (string, int) {
		t.Helper()
		w := couponCheckout(a, catalog.admin, key, "", map[string]any{"variantId": catalog.variantA, "quantity": 2}, map[string]any{"variantId": catalog.variantB, "quantity": 1})
		if w.Code != http.StatusOK {
			t.Fatalf("multi-item checkout: %d %s", w.Code, w.Body.String())
		}
		var result struct {
			Data struct {
				OrderID string `json:"orderId"`
				Total   int    `json:"totalAmount"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Data.Total != 360 {
			t.Fatalf("multi-item total=%d, want 360", result.Data.Total)
		}
		return result.Data.OrderID, result.Data.Total
	}
	setExpired := func(orderID string) {
		t.Helper()
		if _, err := a.db.Exec("UPDATE shop_inventory_reservations SET expires_at='2000-01-01T00:00:00Z' WHERE organization_id=? AND cart_id=?", catalog.orgID, orderID); err != nil {
			t.Fatal(err)
		}
	}
	checkReservations := func(orderID, expected string) {
		t.Helper()
		var total, matching int
		if err := a.db.QueryRow("SELECT count(*),COALESCE(sum(CASE WHEN status=? THEN 1 ELSE 0 END),0) FROM shop_inventory_reservations WHERE organization_id=? AND cart_id=? AND session_id LIKE 'shop-go-checkout:%'", expected, catalog.orgID, orderID).Scan(&total, &matching); err != nil || total != 2 || matching != 2 {
			t.Fatalf("reservation states order=%s total=%d %s=%d err=%v", orderID, total, expected, matching, err)
		}
	}
	callback := func(orderID, reference string, amount int) *httptest.ResponseRecorder {
		return request(a, http.MethodPost, "/api/stations/shop/payment/callback", map[string]any{"orderId": orderID, "attemptReference": reference, "amount": amount, "status": "success"}, catalog.admin...)
	}
	paidOrder, paidAmount := makeOrder("multi-paid-checkout-01")
	if w := callback(paidOrder, "multi-paid-attempt-01", paidAmount); w.Code != http.StatusOK {
		t.Fatalf("multi-item payment: %d %s", w.Code, w.Body.String())
	}
	checkReservations(paidOrder, "consumed")
	var paidState string
	if err := a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE organization_id=? AND id=?", catalog.orgID, paidOrder).Scan(&paidState); err != nil || paidState != "paid" {
		t.Fatalf("multi-item paid state=%s err=%v", paidState, err)
	}
	expiredOrder, expiredAmount := makeOrder("multi-expired-checkout-01")
	setExpired(expiredOrder)
	if w := callback(expiredOrder, "multi-expired-attempt-01", expiredAmount); w.Code != http.StatusConflict {
		t.Fatalf("multi-item expired payment: %d %s", w.Code, w.Body.String())
	}
	checkReservations(expiredOrder, "expired")
	var expiredState string
	if err := a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE organization_id=? AND id=?", catalog.orgID, expiredOrder).Scan(&expiredState); err != nil || expiredState != "expired" {
		t.Fatalf("multi-item expired state=%s err=%v", expiredState, err)
	}
	var stockA, stockB int
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantA).Scan(&stockA); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantB).Scan(&stockB); err != nil || stockA != 18 || stockB != 19 {
		t.Fatalf("multi-item expiry did not restore all stock: A=%d B=%d err=%v", stockA, stockB, err)
	}
	raceOrder, raceAmount := makeOrder("multi-race-checkout-01")
	setExpired(raceOrder)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := callback(raceOrder, fmt.Sprintf("multi-race-attempt-%02d", i), raceAmount)
			if w.Code != http.StatusConflict {
				t.Errorf("expired multi-item race callback status=%d body=%s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	checkReservations(raceOrder, "expired")
	if err := a.db.QueryRow("SELECT payment_status FROM shop_orders WHERE organization_id=? AND id=?", catalog.orgID, raceOrder).Scan(&expiredState); err != nil || expiredState != "expired" {
		t.Fatalf("multi-item race state=%s err=%v", expiredState, err)
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantA).Scan(&stockA); err != nil {
		t.Fatal(err)
	}
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantB).Scan(&stockB); err != nil || stockA != 18 || stockB != 19 {
		t.Fatalf("multi-item expiration race restored stock incorrectly: A=%d B=%d err=%v", stockA, stockB, err)
	}
}

func TestShopCouponFreshAndV2SchemaUpgradeIsIdempotent(t *testing.T) {
	fresh, err := openDB(filepath.Join(t.TempDir(), "fresh-coupon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var freshVersion int
	var couponColumn int
	if err = fresh.QueryRow("SELECT version FROM shop_repository_schema").Scan(&freshVersion); err != nil {
		t.Fatal(err)
	}
	if freshVersion != 3 {
		t.Fatalf("fresh schema version=%d, want 3", freshVersion)
	}
	if err = fresh.QueryRow("SELECT count(*) FROM pragma_table_info('shop_orders') WHERE name='coupon_code'").Scan(&couponColumn); err != nil || couponColumn != 1 {
		t.Fatalf("fresh database coupon_code column=%d err=%v", couponColumn, err)
	}
	if err = fresh.Close(); err != nil {
		t.Fatal(err)
	}
	v2Path := filepath.Join(t.TempDir(), "v2-coupon.sqlite")
	v2, err := openDB(v2Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v2.Exec("INSERT INTO shop_orders(id,organization_id,order_number,customer_email,shipping_address_json,subtotal,discount_amount,shipping_fee,total_amount) VALUES('existing-order','existing-org','EXISTING-01','existing@example.test','{}',250,25,60,285)"); err != nil {
		t.Fatal(err)
	}
	if _, err = v2.Exec("ALTER TABLE shop_orders DROP COLUMN coupon_code; UPDATE shop_repository_schema SET version=2"); err != nil {
		t.Fatal(err)
	}
	if err = v2.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		upgraded, openErr := openDB(v2Path)
		if openErr != nil {
			t.Fatalf("open v2 upgrade pass %d: %v", i, openErr)
		}
		if openErr = upgraded.QueryRow("SELECT count(*) FROM pragma_table_info('shop_orders') WHERE name='coupon_code'").Scan(&couponColumn); openErr != nil || couponColumn != 1 {
			t.Fatalf("upgrade coupon_code column=%d err=%v", couponColumn, openErr)
		}
		var existingTotal int
		if openErr = upgraded.QueryRow("SELECT total_amount FROM shop_orders WHERE id='existing-order'").Scan(&existingTotal); openErr != nil || existingTotal != 285 {
			t.Fatalf("existing order was not preserved: total=%d err=%v", existingTotal, openErr)
		}
		if openErr = upgraded.Close(); openErr != nil {
			t.Fatal(openErr)
		}
	}
}

func addCouponWithUsageLimit(t *testing.T, a *App, org, code, kind string, value, minSpend int, expires any, active int, usageLimit any) {
	t.Helper()
	if _, err := a.db.Exec("INSERT INTO shop_promotions(id,organization_id,code,description,discount_type,discount_value,min_spend,expires_at,is_active,usage_limit,times_used) VALUES(?,?,?,?,?,?,?,?,?,?,0)", "coupon-promo-"+token(), org, code, "coupon test", kind, value, minSpend, expires, active, usageLimit); err != nil {
		t.Fatal(err)
	}
}

func TestShopCouponTenDollarThreeItemAllocationConservesRemainder(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	var productID string
	if err := a.db.QueryRow("SELECT product_id FROM shop_variants WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantA).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	variantC := "coupon-variant-c-" + token()
	if _, err := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,20,0,1)", variantC, catalog.orgID, productID, "SKU-"+variantC, "Variant C", 100); err != nil {
		t.Fatal(err)
	}
	addCouponTestPromotion(t, a, catalog.orgID, "SAVE10FIXED", "fixed", 10, 100, nil, 1)
	w := couponCheckout(a, catalog.admin, "coupon-three-item-01", "SAVE10FIXED",
		map[string]any{"variantId": catalog.variantA, "quantity": 1},
		map[string]any{"variantId": catalog.variantB, "quantity": 1},
		map[string]any{"variantId": variantC, "quantity": 1},
	)
	if w.Code != http.StatusOK {
		t.Fatalf("three-item coupon checkout: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Data struct {
			OrderID        string `json:"orderId"`
			Subtotal       int    `json:"subtotal"`
			DiscountAmount int    `json:"discountAmount"`
			ShippingFee    int    `json:"shippingFee"`
			TotalAmount    int    `json:"totalAmount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Data.Subtotal != 300 || res.Data.DiscountAmount != 10 || res.Data.ShippingFee != 60 || res.Data.TotalAmount != 350 {
		t.Fatalf("unexpected order totals: %+v", res.Data)
	}
	rows, err := a.db.Query("SELECT line_total FROM shop_order_items WHERE organization_id=? AND order_id=? ORDER BY line_total DESC", catalog.orgID, res.Data.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lineTotals []int
	var totalLineSum int
	for rows.Next() {
		var lt int
		if err := rows.Scan(&lt); err != nil {
			t.Fatal(err)
		}
		lineTotals = append(lineTotals, lt)
		totalLineSum += lt
	}
	if totalLineSum != 290 {
		t.Fatalf("sum of line totals = %d, want 290", totalLineSum)
	}
	if len(lineTotals) != 3 || lineTotals[0] != 97 || lineTotals[1] != 97 || lineTotals[2] != 96 {
		t.Fatalf("line totals = %v, want [97, 97, 96]", lineTotals)
	}
}

func TestShopCouponUsageLimitAtomicDecrementAndOverUsagePrevention(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	addCouponWithUsageLimit(t, a, catalog.orgID, "LIMITED2", "fixed", 20, 100, nil, 1, 2)

	w1 := couponCheckout(a, catalog.admin, "limited-checkout-01", "LIMITED2", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	if w1.Code != http.StatusOK {
		t.Fatalf("first checkout with limited coupon: %d %s", w1.Code, w1.Body.String())
	}
	var usageLimit, timesUsed int
	if err := a.db.QueryRow("SELECT COALESCE(usage_limit,0), times_used FROM shop_promotions WHERE organization_id=? AND code='LIMITED2'", catalog.orgID).Scan(&usageLimit, &timesUsed); err != nil {
		t.Fatal(err)
	}
	if usageLimit != 1 || timesUsed != 1 {
		t.Fatalf("after first checkout: usageLimit=%d timesUsed=%d, want 1, 1", usageLimit, timesUsed)
	}

	w2 := couponCheckout(a, catalog.admin, "limited-checkout-02", "LIMITED2", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	if w2.Code != http.StatusOK {
		t.Fatalf("second checkout with limited coupon: %d %s", w2.Code, w2.Body.String())
	}
	if err := a.db.QueryRow("SELECT COALESCE(usage_limit,0), times_used FROM shop_promotions WHERE organization_id=? AND code='LIMITED2'", catalog.orgID).Scan(&usageLimit, &timesUsed); err != nil {
		t.Fatal(err)
	}
	if usageLimit != 0 || timesUsed != 2 {
		t.Fatalf("after second checkout: usageLimit=%d timesUsed=%d, want 0, 2", usageLimit, timesUsed)
	}

	w3 := couponCheckout(a, catalog.admin, "limited-checkout-03", "LIMITED2", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	if w3.Code != http.StatusConflict {
		t.Fatalf("third checkout when quota exhausted: %d %s, want 409", w3.Code, w3.Body.String())
	}

	addCouponWithUsageLimit(t, a, catalog.orgID, "RACE1", "fixed", 10, 100, nil, 1, 1)
	const concurrentUsers = 8
	var wg sync.WaitGroup
	statusCodes := make(chan int, concurrentUsers)
	for i := 0; i < concurrentUsers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := couponCheckout(a, catalog.admin, fmt.Sprintf("race-coupon-%d", idx), "RACE1", map[string]any{"variantId": catalog.variantA, "quantity": 1})
			statusCodes <- w.Code
		}(i)
	}
	wg.Wait()
	close(statusCodes)

	successes, conflicts := 0, 0
	for code := range statusCodes {
		if code == http.StatusOK {
			successes++
		} else if code == http.StatusConflict {
			conflicts++
		} else {
			t.Errorf("unexpected status code: %d", code)
		}
	}
	if successes != 1 || conflicts != concurrentUsers-1 {
		t.Fatalf("concurrent coupon race: successes=%d conflicts=%d, want 1 success and %d conflicts", successes, conflicts, concurrentUsers-1)
	}
	if err := a.db.QueryRow("SELECT COALESCE(usage_limit,0), times_used FROM shop_promotions WHERE organization_id=? AND code='RACE1'", catalog.orgID).Scan(&usageLimit, &timesUsed); err != nil {
		t.Fatal(err)
	}
	if usageLimit != 0 || timesUsed != 1 {
		t.Fatalf("after concurrent race: usageLimit=%d timesUsed=%d, want 0, 1", usageLimit, timesUsed)
	}
}

func TestShopConcurrentCheckoutAtomicInventoryDecrementPreventsOversell(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	if _, err := a.db.Exec("UPDATE shop_variants SET stock_quantity=2, reserved_quantity=0 WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantA); err != nil {
		t.Fatal(err)
	}

	const competitors = 8
	var wg sync.WaitGroup
	results := make(chan int, competitors)
	for i := 0; i < competitors; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			w := couponCheckout(a, catalog.admin, fmt.Sprintf("stock-race-%d", idx), "", map[string]any{"variantId": catalog.variantA, "quantity": 1})
			results <- w.Code
		}(i)
	}
	wg.Wait()
	close(results)

	successes, conflicts := 0, 0
	for code := range results {
		if code == http.StatusOK {
			successes++
		} else if code == http.StatusConflict {
			conflicts++
		} else {
			t.Errorf("unexpected stock race status code: %d", code)
		}
	}
	if successes != 2 || conflicts != competitors-2 {
		t.Fatalf("stock race results: successes=%d conflicts=%d, want 2 successes and %d conflicts", successes, conflicts, competitors-2)
	}
	var finalStock int
	if err := a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", catalog.orgID, catalog.variantA).Scan(&finalStock); err != nil {
		t.Fatal(err)
	}
	if finalStock != 0 {
		t.Fatalf("final stock = %d, want 0", finalStock)
	}
}

func TestShopCheckoutIdempotencyDuplicateAndTamperRejection(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)
	foreign := seedCouponTestCatalog(t, a)

	key := "idempotent-checkout-test-01"
	w1 := couponCheckout(a, catalog.admin, key, "", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	if w1.Code != http.StatusOK {
		t.Fatalf("initial checkout: %d %s", w1.Code, w1.Body.String())
	}
	var res1 struct {
		Data struct {
			OrderID   string `json:"orderId"`
			Duplicate bool   `json:"duplicate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w1.Body.Bytes(), &res1); err != nil || res1.Data.OrderID == "" || res1.Data.Duplicate {
		t.Fatalf("initial result: %+v err=%v", res1, err)
	}

	w2 := couponCheckout(a, catalog.admin, key, "", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	if w2.Code != http.StatusOK {
		t.Fatalf("replay checkout: %d %s", w2.Code, w2.Body.String())
	}
	var res2 struct {
		Data struct {
			OrderID   string `json:"orderId"`
			Duplicate bool   `json:"duplicate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &res2); err != nil || res2.Data.OrderID != res1.Data.OrderID || !res2.Data.Duplicate {
		t.Fatalf("replay result: %+v err=%v", res2, err)
	}

	tamperedQty := couponCheckout(a, catalog.admin, key, "", map[string]any{"variantId": catalog.variantA, "quantity": 2})
	if tamperedQty.Code != http.StatusConflict {
		t.Fatalf("tampered quantity status: %d %s, want 409", tamperedQty.Code, tamperedQty.Body.String())
	}

	addCouponTestPromotion(t, a, catalog.orgID, "IDEM-SAVE", "fixed", 5, 50, nil, 1)
	tamperedCoupon := couponCheckout(a, catalog.admin, key, "IDEM-SAVE", map[string]any{"variantId": catalog.variantA, "quantity": 1})
	if tamperedCoupon.Code != http.StatusConflict {
		t.Fatalf("tampered coupon status: %d %s, want 409", tamperedCoupon.Code, tamperedCoupon.Body.String())
	}

	wForeign := couponCheckout(a, foreign.admin, key, "", map[string]any{"variantId": foreign.variantA, "quantity": 1})
	if wForeign.Code != http.StatusOK {
		t.Fatalf("foreign tenant using same idempotency key: %d %s", wForeign.Code, wForeign.Body.String())
	}
	var resForeign struct {
		Data struct {
			OrderID   string `json:"orderId"`
			Duplicate bool   `json:"duplicate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(wForeign.Body.Bytes(), &resForeign); err != nil || resForeign.Data.OrderID == res1.Data.OrderID || resForeign.Data.Duplicate {
		t.Fatalf("foreign tenant should create independent order: %+v", resForeign)
	}
}

func TestShopRefundRejectsOverRefundAndWritesOutbox(t *testing.T) {
	a := appForTest(t)
	catalog := seedCouponTestCatalog(t, a)

	w := couponCheckout(a, catalog.admin, "refund-test-order-key", "", map[string]any{"variantId": catalog.variantA, "quantity": 2})
	if w.Code != http.StatusOK {
		t.Fatalf("checkout for refund: %d %s", w.Code, w.Body.String())
	}
	var orderRes struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &orderRes); err != nil {
		t.Fatal(err)
	}
	orderID := orderRes.Data.OrderID
	var itemID string
	if err := a.db.QueryRow("SELECT id FROM shop_order_items WHERE organization_id=? AND order_id=?", catalog.orgID, orderID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}

	pendingRefund := refundRequest(a, orderID, itemID, 1, "Early return", "refund-pending-key", catalog.admin...)
	if pendingRefund.Code != http.StatusConflict {
		t.Fatalf("refund on unpaid order status: %d %s, want 409", pendingRefund.Code, pendingRefund.Body.String())
	}

	if _, err := a.db.Exec("UPDATE shop_orders SET payment_status='paid' WHERE organization_id=? AND id=?", catalog.orgID, orderID); err != nil {
		t.Fatal(err)
	}

	r1 := refundRequest(a, orderID, itemID, 1, "Partial return unit 1", "refund-key-01", catalog.admin...)
	if r1.Code != http.StatusOK {
		t.Fatalf("first refund: %d %s", r1.Code, r1.Body.String())
	}

	var auditCount, outboxCount int
	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action='shop.refund.create'", catalog.orgID, orderID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit count = %d, want 1 (err=%v)", auditCount, err)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='shop.refunded' AND payload LIKE ?", catalog.orgID, "%"+orderID+"%").Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox count = %d, want 1 (err=%v)", outboxCount, err)
	}

	rOver := refundRequest(a, orderID, itemID, 2, "Over refund return", "refund-key-over", catalog.admin...)
	if rOver.Code != http.StatusConflict {
		t.Fatalf("over-refund status: %d %s, want 409", rOver.Code, rOver.Body.String())
	}

	r2 := refundRequest(a, orderID, itemID, 1, "Partial return unit 2", "refund-key-02", catalog.admin...)
	if r2.Code != http.StatusOK {
		t.Fatalf("second refund: %d %s", r2.Code, r2.Body.String())
	}

	rExcess := refundRequest(a, orderID, itemID, 1, "Excess refund", "refund-key-excess", catalog.admin...)
	if rExcess.Code != http.StatusConflict {
		t.Fatalf("excess refund status: %d %s, want 409", rExcess.Code, rExcess.Body.String())
	}

	if err := a.db.QueryRow("SELECT count(*) FROM audit_events WHERE organization_id=? AND entity_id=? AND action='shop.refund.create'", catalog.orgID, orderID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("final audit count = %d, want 2", auditCount)
	}
	if err := a.db.QueryRow("SELECT count(*) FROM outbox_events WHERE organization_id=? AND event_type='shop.refunded' AND payload LIKE ?", catalog.orgID, "%"+orderID+"%").Scan(&outboxCount); err != nil || outboxCount != 2 {
		t.Fatalf("final outbox count = %d, want 2", outboxCount)
	}
}

func TestShopCrossTenantCheckoutAndRefundIsolation(t *testing.T) {
	a := appForTest(t)
	tenantA := seedCouponTestCatalog(t, a)
	tenantB := seedCouponTestCatalog(t, a)

	addCouponTestPromotion(t, a, tenantA.orgID, "TENANT-A-COUPON", "percentage", 10, 50, nil, 1)

	wCrossCoupon := couponCheckout(a, tenantB.admin, "cross-coupon-test", "TENANT-A-COUPON", map[string]any{"variantId": tenantB.variantA, "quantity": 1})
	if wCrossCoupon.Code != http.StatusConflict {
		t.Fatalf("tenant B using tenant A's coupon: %d %s, want 409", wCrossCoupon.Code, wCrossCoupon.Body.String())
	}

	wCrossVariant := couponCheckout(a, tenantB.admin, "cross-variant-test", "", map[string]any{"variantId": tenantA.variantA, "quantity": 1})
	if wCrossVariant.Code != http.StatusNotFound {
		t.Fatalf("tenant B purchasing tenant A's variant: %d %s, want 404", wCrossVariant.Code, wCrossVariant.Body.String())
	}

	wOrder := couponCheckout(a, tenantA.admin, "tenant-a-order-key", "", map[string]any{"variantId": tenantA.variantA, "quantity": 1})
	if wOrder.Code != http.StatusOK {
		t.Fatalf("tenant A checkout: %d %s", wOrder.Code, wOrder.Body.String())
	}
	var res struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	_ = json.Unmarshal(wOrder.Body.Bytes(), &res)
	orderID := res.Data.OrderID
	var itemID string
	_ = a.db.QueryRow("SELECT id FROM shop_order_items WHERE organization_id=? AND order_id=?", tenantA.orgID, orderID).Scan(&itemID)
	_, _ = a.db.Exec("UPDATE shop_orders SET payment_status='paid' WHERE organization_id=? AND id=?", tenantA.orgID, orderID)

	wCrossRefund := refundRequest(a, orderID, itemID, 1, "Cross tenant return attempt", "cross-refund-key", tenantB.admin...)
	if wCrossRefund.Code != http.StatusNotFound {
		t.Fatalf("tenant B refunding tenant A's order: %d %s, want 404", wCrossRefund.Code, wCrossRefund.Body.String())
	}
}

