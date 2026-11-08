package main

import (
	"encoding/json"
	"testing"
)

func TestShopFrontCatalogExcludesInactive(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	front := loginSite(t, a, "front@suite.test", space, "shop")
	var product, variant string
	if err := a.db.QueryRow("SELECT p.id,v.id FROM shop_products p JOIN shop_variants v ON v.product_id=p.id AND v.organization_id=p.organization_id WHERE p.organization_id=? LIMIT 1", org).Scan(&product, &variant); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE shop_variants SET is_active=0 WHERE id=? AND organization_id=?", variant, org); err != nil {
		t.Fatal(err)
	}
	w := request(a, "GET", "/api/stations/shop/data?view=front", nil, front...)
	var result struct {
		Data struct {
			Variants []struct {
				ID string `json:"id"`
			} `json:"variants"`
			Products []struct {
				ID string `json:"id"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatalf("data %d %v", w.Code, err)
	}
	for _, v := range result.Data.Variants {
		if v.ID == variant {
			t.Fatal("inactive variant exposed")
		}
	}
	if _, err := a.db.Exec("UPDATE shop_products SET is_active=0 WHERE id=? AND organization_id=?", product, org); err != nil {
		t.Fatal(err)
	}
	w = request(a, "GET", "/api/stations/shop/data?view=front", nil, front...)
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 {
		t.Fatalf("data %d %v", w.Code, err)
	}
	for _, p := range result.Data.Products {
		if p.ID == product {
			t.Fatal("inactive product exposed")
		}
	}
}
