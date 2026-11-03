package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func TestShopCheckoutLabCreatesRealOrdersWithoutOversell(t *testing.T) {
	a := appForTest(t)
	admin := login(t, a, "admin@suite.test", token())
	w := request(a, http.MethodPost, "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 5}, admin...)
	if w.Code != 200 {
		t.Fatalf("prepare: %d %s", w.Code, w.Body.String())
	}
	var prepared struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &prepared) != nil || prepared.Data.RunID == "" {
		t.Fatal(w.Body.String())
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted, duplicates, soldOut := 0, 0, 0
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("buyer-%d", i)
			if i%4 == 0 {
				key = fmt.Sprintf("buyer-%d", i-1)
			}
			res := request(a, http.MethodPost, "/api/stations/shop/checkout-lab", map[string]any{"action": "checkout", "runId": prepared.Data.RunID, "key": key}, admin...)
			var body struct {
				Data struct {
					Outcome string `json:"outcome"`
					OrderID string `json:"orderId"`
				} `json:"data"`
			}
			_ = json.Unmarshal(res.Body.Bytes(), &body)
			mu.Lock()
			defer mu.Unlock()
			if res.Code != 200 {
				t.Errorf("checkout: %d %s", res.Code, res.Body.String())
				return
			}
			switch body.Data.Outcome {
			case "accepted":
				accepted++
				if body.Data.OrderID == "" {
					t.Error("accepted without order")
				}
			case "duplicate":
				duplicates++
			case "sold_out":
				soldOut++
			default:
				t.Errorf("outcome %q", body.Data.Outcome)
			}
		}(i)
	}
	wg.Wait()
	if accepted != 5 || duplicates == 0 || soldOut == 0 {
		t.Fatalf("accepted=%d duplicate=%d soldout=%d", accepted, duplicates, soldOut)
	}
	var orders int
	if e := a.db.QueryRow("SELECT count(*) FROM shop_orders o JOIN shop_checkout_lab_runs r ON r.organization_id=o.organization_id JOIN shop_checkout_lab_orders l ON l.order_id=o.id WHERE r.id=?", prepared.Data.RunID).Scan(&orders); e != nil || orders != 5 {
		t.Fatalf("real order count=%d err=%v", orders, e)
	}
	var stock int
	if e := a.db.QueryRow("SELECT v.stock_quantity FROM shop_variants v JOIN shop_checkout_lab_runs r ON r.variant_id=v.id WHERE r.id=?", prepared.Data.RunID).Scan(&stock); e != nil || stock != 0 {
		t.Fatalf("remaining stock=%d err=%v", stock, e)
	}
}

func TestShopCheckoutLabPermissionAndTenantIsolation(t *testing.T) {
	a := appForTest(t)
	admin := login(t, a, "admin@suite.test", token())
	other := login(t, a, "admin@suite.test", token())
	viewer := login(t, a, "viewer@suite.test", token())
	w := request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 2}, admin...)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var d struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if w = request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "checkout", "runId": d.Data.RunID, "key": "x"}, other...); w.Code != 404 {
		t.Fatalf("cross tenant: %d", w.Code)
	}
	if w = request(a, "POST", "/api/stations/shop/checkout-lab", map[string]any{"action": "prepare", "capacity": 2}, viewer...); w.Code != 403 {
		t.Fatalf("readonly: %d", w.Code)
	}
}
