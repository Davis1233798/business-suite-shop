package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestOperationsSnapshotTenantAndScope(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	front := loginSite(t, a, "front@suite.test", space, "shop")
	other := loginSite(t, a, "admin@suite.test", token(), "shop")
	if _, e := a.db.Exec("INSERT INTO outbox_events(id,organization_id,event_type,payload,status,retry_count) VALUES('test-event',?,'shop.paid','{}','pending',2)", org); e != nil {
		t.Fatal(e)
	}
	path := "/api/stations/shop/operations-snapshot"
	if w := request(a, "GET", path, nil, front...); w.Code != 403 {
		t.Fatalf("front %d", w.Code)
	}
	read := func(cookies ...*http.Cookie) map[string]int64 {
		w := request(a, "GET", path, nil, cookies...)
		var v struct {
			Data map[string]int64 `json:"data"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil || w.Code != 200 {
			t.Fatalf("snapshot %d %v", w.Code, e)
		}
		return v.Data
	}
	if data := read(admin...); data["pendingOutbox"] != 1 || data["retryingOutbox"] != 1 {
		t.Fatalf("own snapshot %+v", data)
	}
	if data := read(other...); data["pendingOutbox"] != 0 {
		t.Fatalf("foreign snapshot %+v", data)
	}
	if w := request(a, "POST", path, nil, admin...); w.Code != 405 {
		t.Fatalf("method %d", w.Code)
	}
}

func TestShopSnapshotOnlyCountsRefundsLinkedToPaidOrderItems(t *testing.T) {
	a := appForTest(t)
	space := token()
	org := "org_test_" + space
	admin := loginSite(t, a, "admin@suite.test", space, "shop")
	variant := token()
	if _, e := a.db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity) VALUES(?,?,'prod','SKU','Title',100,10,0)", variant, org); e != nil {
		t.Fatal(e)
	}
	orderPaid := token()
	if _, e := a.db.Exec("INSERT INTO shop_orders(id,organization_id,order_number,customer_email,shipping_address_json,subtotal,total_amount,payment_status) VALUES(?,?,?,'buyer@suite.test','{}',100,100,'paid')", orderPaid, org, "ORD-PAID-001"); e != nil {
		t.Fatal(e)
	}
	itemPaid := token()
	if _, e := a.db.Exec("INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,100,1,100)", itemPaid, org, orderPaid, variant, "Prod", "Title", "SKU"); e != nil {
		t.Fatal(e)
	}
	orderPending := token()
	if _, e := a.db.Exec("INSERT INTO shop_orders(id,organization_id,order_number,customer_email,shipping_address_json,subtotal,total_amount,payment_status) VALUES(?,?,?,'buyer@suite.test','{}',100,100,'pending')", orderPending, org, "ORD-PEND-001"); e != nil {
		t.Fatal(e)
	}
	itemPending := token()
	if _, e := a.db.Exec("INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,100,1,100)", itemPending, org, orderPending, variant, "Prod", "Title", "SKU"); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO shop_returns(id,organization_id,order_id,order_item_id,quantity,reason,status,refund_amount) VALUES(?,?,?,?,1,'return','refunded',100)", token(), org, orderPaid, itemPaid); e != nil {
		t.Fatal(e)
	}
	if _, e := a.db.Exec("INSERT INTO shop_returns(id,organization_id,order_id,order_item_id,quantity,reason,status,refund_amount) VALUES(?,?,?,?,1,'return','refunded',100)", token(), org, orderPending, itemPending); e != nil {
		t.Fatal(e)
	}
	w := request(a, "GET", "/api/stations/shop/operations-snapshot", nil, admin...)
	var v struct {
		Data map[string]int64 `json:"data"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &v); e != nil || w.Code != 200 {
		t.Fatalf("snapshot %d %v", w.Code, e)
	}
	if v.Data["refundedAmount"] != 100 {
		t.Fatalf("refundedAmount want 100, got %d", v.Data["refundedAmount"])
	}
}
