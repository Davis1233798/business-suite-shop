package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestShopStorefrontFrontProjectionAndCheckoutOwnership(t *testing.T) {
	a := appForTest(t)
	space := token()
	front := loginSite(t, a, "front@suite.test", space, "shop")
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	readonly := loginSite(t, a, "viewer@suite.test", space, "shop")
	identityResponse := request(a, http.MethodGet, "/api/auth/me?site=shop", nil, front...)
	var identityEnvelope struct {
		Data Identity `json:"data"`
	}
	if identityResponse.Code != http.StatusOK || json.Unmarshal(identityResponse.Body.Bytes(), &identityEnvelope) != nil {
		t.Fatalf("read storefront identity: %d %s", identityResponse.Code, identityResponse.Body.String())
	}
	var variantIDs []string
	rows, err := a.db.Query("SELECT id FROM shop_variants WHERE organization_id=? AND is_active=1 ORDER BY id LIMIT 2", identityEnvelope.Data.OrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		variantIDs = append(variantIDs, id)
	}
	if err = rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(variantIDs) != 2 {
		t.Fatalf("want two active variants, got %d", len(variantIDs))
	}
	address := map[string]string{"name": "Buyer", "city": "Taipei", "address": "Demo Road 1", "phone": "0900000000"}
	input := map[string]any{
		"items":         []map[string]any{{"variantId": variantIDs[0], "quantity": 1}, {"variantId": variantIDs[1], "quantity": 1}},
		"customerEmail": "front@example.test", "shippingAddress": address, "couponCode": "SAVE10", "idempotencyKey": "storefront-multi-item-001",
	}
	checkout := func(cookies []*http.Cookie, payload map[string]any) map[string]any {
		t.Helper()
		w := request(a, http.MethodPost, "/api/stations/shop/checkout", payload, cookies...)
		if w.Code != http.StatusOK {
			t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	frontOrder := checkout(front, input)
	if frontOrder["paymentStatus"] != "pending" || frontOrder["discountAmount"].(float64) <= 0 {
		t.Fatalf("checkout did not use server coupon pricing: %#v", frontOrder)
	}
	frontReplay := checkout(front, input)
	if frontReplay["orderId"] != frontOrder["orderId"] || frontReplay["duplicate"] != true {
		t.Fatalf("idempotent replay did not return the original order: %#v", frontReplay)
	}
	adminInput := map[string]any{
		"items":         []map[string]any{{"variantId": variantIDs[0], "quantity": 1}},
		"customerEmail": "admin@example.test", "shippingAddress": address, "idempotencyKey": "storefront-other-buyer-001",
	}
	adminOrder := checkout(admin, adminInput)
	if adminOrder["orderId"] == frontOrder["orderId"] {
		t.Fatal("separate buyers unexpectedly received the same order")
	}
	var categoryID string
	if err := a.db.QueryRow("SELECT p.category_id FROM shop_variants v JOIN shop_products p ON p.id=v.product_id WHERE v.id=? AND p.organization_id=?", variantIDs[0], identityEnvelope.Data.OrganizationID).Scan(&categoryID); err != nil {
		t.Fatal(err)
	}
	inactiveProductID, inactiveVariantID := token(), token()
	if _, err := a.db.Exec("INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,0)", inactiveProductID, identityEnvelope.Data.OrganizationID, "Hidden product", inactiveProductID, "Inactive parent", categoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,100,50,0,1)", inactiveVariantID, identityEnvelope.Data.OrganizationID, inactiveProductID, inactiveVariantID, "Hidden variant"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE shop_variants SET reserved_quantity=4 WHERE id=? AND organization_id=?", variantIDs[0], identityEnvelope.Data.OrganizationID); err != nil {
		t.Fatal(err)
	}
	if w := request(a, http.MethodPost, "/api/stations/shop/checkout", input); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous checkout accepted: %d", w.Code)
	}
	if w := request(a, http.MethodPost, "/api/stations/shop/checkout", input, readonly...); w.Code != http.StatusForbidden {
		t.Fatalf("readonly checkout accepted: %d", w.Code)
	}
	readFront := func(cookies []*http.Cookie) map[string][]map[string]any {
		t.Helper()
		w := request(a, http.MethodGet, "/api/stations/shop/data?view=front", nil, cookies...)
		if w.Code != http.StatusOK {
			t.Fatalf("front data: %d %s", w.Code, w.Body.String())
		}
		var envelope struct {
			Data map[string][]map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	frontData := readFront(front)
	if len(frontData["products"]) == 0 || len(frontData["variants"]) < 2 || len(frontData["categories"]) == 0 {
		t.Fatalf("storefront catalog projection incomplete: %#v", frontData)
	}
	for _, product := range frontData["products"] {
		if product["id"] == inactiveProductID {
			t.Fatalf("inactive product leaked into storefront catalog: %#v", product)
		}
	}
	for _, variant := range frontData["variants"] {
		if variant["id"] == inactiveVariantID || variant["productId"] == inactiveProductID {
			t.Fatalf("inactive product variant leaked into storefront catalog: %#v", variant)
		}
	}
	available := false
	for _, variant := range frontData["variants"] {
		if variant["id"] == variantIDs[0] {
			available = variant["availableQuantity"] == float64(14)
			for _, unnecessary := range []string{"stockQuantity", "reservedQuantity", "sku", "isActive", "organizationId"} {
				if _, exists := variant[unnecessary]; exists {
					t.Errorf("storefront exposed unnecessary field %s", unnecessary)
				}
			}
		}
	}
	if !available {
		t.Fatalf("storefront did not expose stock minus reservations: %#v", frontData["variants"])
	}
	if len(frontData["orders"]) != 1 || frontData["orders"][0]["id"] != frontOrder["orderId"] {
		t.Fatalf("front buyer saw orders outside their ownership: %#v", frontData["orders"])
	}
	if len(frontData["items"]) != 2 {
		t.Fatalf("front buyer should see only their two order lines, got %#v", frontData["items"])
	}
	for _, forbidden := range []string{"customerEmail", "shippingAddressJson", "userId", "organizationId"} {
		if _, exists := frontData["orders"][0][forbidden]; exists {
			t.Errorf("front order projection leaked %s", forbidden)
		}
	}
	adminFrontData := readFront(admin)
	if len(adminFrontData["orders"]) != 1 || adminFrontData["orders"][0]["id"] != adminOrder["orderId"] {
		t.Fatalf("back-office identity using front projection saw another buyer's order: %#v", adminFrontData["orders"])
	}
	back := request(a, http.MethodGet, "/api/stations/shop/data?view=back", nil, admin...)
	if back.Code != http.StatusOK {
		t.Fatalf("back office data unavailable: %d %s", back.Code, back.Body.String())
	}
}
