package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

type couponTestCatalog struct {
	orgID    string
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
	return couponTestCatalog{orgID: org, admin: admin, variantA: variants[0], variantB: variants[1]}
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
	addCouponTestPromotion(t, foreign.orgID, "FOREIGN", "fixed", 1, 0, nil, 1)
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
