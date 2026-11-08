package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestShopCartShippingAddressAndPrivacy(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	buyer := loginSite(t, a, "front@suite.test", space, "shop")
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	other := loginSite(t, a, "office@suite.test", space, "shop")
	var variant string
	a.db.QueryRow("SELECT id FROM shop_variants WHERE organization_id=? AND stock_quantity>0 LIMIT 1", org).Scan(&variant)
	path := "/api/stations/shop/cart-checkout"
	address := map[string]any{"name": " Example Recipient ", "city": " Example City ", "address": " Example Road ", "phone": " DEMO-PHONE "}
	input := map[string]any{"lines": []any{map[string]any{"variantId": variant, "quantity": 1}}, "shippingAddress": address, "idempotencyKey": "shipping-cart-0001"}
	w := request(a, "POST", path, input, buyer...)
	if w.Code != 200 {
		t.Fatalf("checkout %d %s", w.Code, w.Body.String())
	}
	var value struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &value)
	var raw string
	if e := a.db.QueryRow("SELECT shipping_address_json FROM shop_orders WHERE organization_id=? AND id=?", org, value.Data.OrderID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var saved shopAddress
	if e := json.Unmarshal([]byte(raw), &saved); e != nil || saved.Name != "Example Recipient" || saved.City != "Example City" || saved.Address != "Example Road" || saved.Phone != "DEMO-PHONE" {
		t.Fatalf("address %+v %v", saved, e)
	}
	front := request(a, "GET", "/api/stations/shop/data?view=front", nil, other...)
	if front.Code != 200 || strings.Contains(front.Body.String(), "Example Road") || strings.Contains(front.Body.String(), value.Data.OrderID) {
		t.Fatal("address or order leaked to another user")
	}
	back := request(a, "GET", "/api/stations/shop/data?view=back", nil, admin...)
	if back.Code != 200 || !strings.Contains(back.Body.String(), "Example Road") {
		t.Fatal("back office cannot see shipping address")
	}
	invalid := map[string]any{"lines": input["lines"], "shippingAddress": map[string]any{"name": "", "city": "C", "address": "A", "phone": "P"}, "idempotencyKey": "shipping-cart-0002"}
	if w := request(a, "POST", path, invalid, buyer...); w.Code != 400 {
		t.Fatalf("invalid address %d", w.Code)
	}
}
