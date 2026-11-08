package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func TestShopAfterSalesOwnershipAndConcurrentReturns(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	buyer := loginSite(t, a, "front@suite.test", space, "shop")
	viewer := loginSite(t, a, "viewer@suite.test", space, "shop")
	other := loginSite(t, a, "admin@suite.test", token(), "shop")
	var variant, product string
	var initial int64
	if e := a.db.QueryRow("SELECT id,product_id,stock_quantity FROM shop_variants WHERE organization_id=? AND stock_quantity=1 LIMIT 1", org).Scan(&variant, &product, &initial); e != nil {
		t.Fatal(e)
	}
	business := "/api/stations/shop/business"
	w := request(a, "POST", business, map[string]any{"action": "checkout", "variantId": variant, "quantity": 1, "idempotencyKey": "after-checkout-01"}, buyer...)
	if w.Code != 200 {
		t.Fatalf("checkout %d %s", w.Code, w.Body.String())
	}
	var v struct {
		Data struct {
			OrderID string `json:"orderId"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	order := v.Data.OrderID
	var item string
	if e := a.db.QueryRow("SELECT id FROM shop_order_items WHERE organization_id=? AND order_id=?", org, order).Scan(&item); e != nil {
		t.Fatal(e)
	}
	pay := map[string]any{"action": "pay", "orderId": order, "idempotencyKey": "after-payment-01"}
	if w := request(a, "POST", business, pay, buyer...); w.Code != 200 {
		t.Fatalf("pay %d", w.Code)
	}
	path := "/api/stations/shop/after-sales"
	refund := func(i int) map[string]any {
		return map[string]any{"action": "refund", "orderId": order, "itemId": item, "quantity": 1, "reason": "Example return", "idempotencyKey": fmt.Sprintf("shop-return-%08d", i)}
	}
	if w := request(a, "POST", path, refund(99), buyer...); w.Code != 403 {
		t.Fatalf("front refund %d", w.Code)
	}
	if w := request(a, "POST", path, refund(99), other...); w.Code != 404 {
		t.Fatalf("foreign refund %d", w.Code)
	}
	if w := request(a, "POST", path, refund(99), admin...); w.Code != 409 {
		t.Fatalf("unshipped refund %d", w.Code)
	}
	ship := map[string]any{"action": "ship", "orderId": order, "tracking": "DEMO-TRACK", "idempotencyKey": "after-shipping-01"}
	if w := request(a, "POST", business, ship, admin...); w.Code != 200 {
		t.Fatalf("ship %d %s", w.Code, w.Body.String())
	}
	review := func(i int) map[string]any {
		return map[string]any{"action": "review", "orderId": order, "productId": product, "rating": 5, "comment": "Example review", "idempotencyKey": fmt.Sprintf("shop-review-%08d", i)}
	}
	if w := request(a, "POST", path, review(99), admin...); w.Code != 403 {
		t.Fatalf("nonbuyer review %d", w.Code)
	}
	if w := request(a, "POST", path, review(99), viewer...); w.Code != 403 {
		t.Fatalf("readonly review %d", w.Code)
	}
	own := request(a, "GET", "/api/stations/shop/data?view=front", nil, buyer...)
	if own.Code != 200 || !strings.Contains(own.Body.String(), item) {
		t.Fatal("buyer cannot see own item")
	}
	foreign := loginSite(t, a, "office@suite.test", space, "shop")
	notOwn := request(a, "GET", "/api/stations/shop/data?view=front", nil, foreign...)
	if notOwn.Code != 200 || strings.Contains(notOwn.Body.String(), item) {
		t.Fatal("other user can see buyer item")
	}
	run := func(makeInput func(int) map[string]any, cookies []*http.Cookie) (int, int, int) {
		var wg sync.WaitGroup
		type outcome struct{ code, index int }
		results := make(chan outcome, 12)
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results <- outcome{request(a, "POST", path, makeInput(i), cookies...).Code, i}
			}(i)
		}
		wg.Wait()
		close(results)
		ok, conflicts, winner := 0, 0, 0
		for r := range results {
			if r.code == 200 {
				ok++
				winner = r.index
			} else if r.code == 409 {
				conflicts++
			} else {
				t.Errorf("unexpected %d", r.code)
			}
		}
		return ok, conflicts, winner
	}
	good, conflict, winner := run(review, buyer)
	if good != 1 || conflict != 11 {
		t.Fatalf("review %d %d", good, conflict)
	}
	if w := request(a, "POST", path, review(winner), buyer...); w.Code != 200 {
		t.Fatalf("review replay %d", w.Code)
	}
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
	good, conflict, winner = run(refund, admin)
	if good != 1 || conflict != 11 {
		t.Fatalf("refund %d %d", good, conflict)
	}
	if w := request(a, "POST", path, refund(winner), admin...); w.Code != 200 {
		t.Fatalf("refund replay %d", w.Code)
	}
	var refundAmount int64
	if e := a.db.QueryRow("SELECT refund_amount FROM shop_returns WHERE organization_id=? AND order_item_id=?", org, item).Scan(&refundAmount); e != nil {
		t.Fatal(e)
	}
	after := snapshot()
	if after["refundedAmount"] != before["refundedAmount"]+refundAmount || after["netRevenue"] != before["netRevenue"]-refundAmount || after["paidRevenue"] != before["paidRevenue"] {
		t.Fatalf("revenue before=%v after=%v refund=%d", before, after, refundAmount)
	}
	var stock, returns, reviews int64
	a.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE organization_id=? AND id=?", org, variant).Scan(&stock)
	a.db.QueryRow("SELECT count(*) FROM shop_returns WHERE organization_id=? AND order_item_id=?", org, item).Scan(&returns)
	a.db.QueryRow("SELECT count(*) FROM shop_reviews WHERE organization_id=? AND order_id=? AND product_id=?", org, order, product).Scan(&reviews)
	if stock != initial || returns != 1 || reviews != 1 {
		t.Fatalf("state stock=%d returns=%d reviews=%d", stock, returns, reviews)
	}
}
