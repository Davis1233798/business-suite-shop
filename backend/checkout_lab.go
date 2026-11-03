package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
)

// checkoutLabAPI exercises real Shop order and variant-stock tables using a
// per-run synthetic product. It never selects a production variant.
func (a *App) checkoutLabAPI(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("site") != "shop" {
		fail(w, 404, "Checkout lab is unavailable")
		return
	}
	id, err := a.identity(r)
	if err != nil {
		fail(w, 401, "Sign in to use checkout lab")
		return
	}
	g := id.grant("shop")
	if g == nil || g.Scope != "back" {
		fail(w, 403, "Back-office access is required")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var in struct {
		Action   string `json:"action"`
		RunID    string `json:"runId"`
		Key      string `json:"key"`
		Capacity int    `json:"capacity"`
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
		return
	}
	if g.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in) != nil {
		fail(w, 400, "Invalid checkout lab request")
		return
	}
	if in.Action == "prepare" {
		if in.Capacity < 1 || in.Capacity > 1000 {
			fail(w, 400, "Capacity must be between 1 and 1000")
			return
		}
		tx, e := a.db.BeginTx(r.Context(), nil)
		if e != nil {
			fail(w, 503, "Database unavailable")
			return
		}
		defer tx.Rollback()
		run, product, variant, category := token(), token(), token(), token()
		_, e = tx.Exec("INSERT INTO shop_categories(id,organization_id,name,slug) VALUES(?,?,?,?)", category, id.OrganizationID, "Concurrency Lab", "lab-"+run)
		if e == nil {
			_, e = tx.Exec("INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", product, id.OrganizationID, "Isolated concurrency checkout", "lab-"+run, "Synthetic product used only by this isolated run", category)
		}
		if e == nil {
			_, e = tx.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,?,0,1)", variant, id.OrganizationID, product, "LAB-"+run[:12], "Lab unit", 100, in.Capacity)
		}
		if e == nil {
			_, e = tx.Exec("INSERT INTO shop_checkout_lab_runs(id,organization_id,variant_id,initial_stock) VALUES(?,?,?,?)", run, id.OrganizationID, variant, in.Capacity)
		}
		if e != nil || tx.Commit() != nil {
			fail(w, 503, "Could not prepare isolated checkout")
			return
		}
		ok(w, map[string]any{"runId": run, "capacity": in.Capacity, "unitPrice": 100})
		return
	}
	if in.Action != "checkout" || in.RunID == "" || len(strings.TrimSpace(in.Key)) < 1 || len(in.Key) > 128 {
		fail(w, 400, "Provide a valid checkout action, run and request key")
		return
	}
	tx, e := a.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 503, "Database unavailable")
		return
	}
	defer tx.Rollback()
	var variant string
	var capacity int
	e = tx.QueryRow("SELECT variant_id,initial_stock FROM shop_checkout_lab_runs WHERE id=? AND organization_id=?", in.RunID, id.OrganizationID).Scan(&variant, &capacity)
	if e == sql.ErrNoRows {
		fail(w, 404, "Checkout run not found in this workspace")
		return
	}
	if e != nil {
		fail(w, 503, "Could not read checkout run")
		return
	}
	var orderID string
	e = tx.QueryRow("SELECT order_id FROM shop_checkout_lab_orders WHERE run_id=? AND request_key=?", in.RunID, strings.TrimSpace(in.Key)).Scan(&orderID)
	if e == nil {
		var stock int
		_ = tx.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=? AND organization_id=?", variant, id.OrganizationID).Scan(&stock)
		if tx.Commit() != nil {
			fail(w, 503, "Checkout read failed")
			return
		}
		ok(w, map[string]any{"outcome": "duplicate", "orderId": orderID, "remaining": stock, "capacity": capacity})
		return
	}
	if e != sql.ErrNoRows {
		fail(w, 503, "Could not check idempotency key")
		return
	}
	result, e := tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity-1 WHERE id=? AND organization_id=? AND is_active=1 AND stock_quantity>0", variant, id.OrganizationID)
	if e != nil {
		fail(w, 503, "Inventory update failed")
		return
	}
	n, e := result.RowsAffected()
	if e != nil {
		fail(w, 503, "Inventory update failed")
		return
	}
	if n == 0 {
		var stock int
		_ = tx.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=? AND organization_id=?", variant, id.OrganizationID).Scan(&stock)
		if tx.Commit() != nil {
			fail(w, 503, "Inventory check failed")
			return
		}
		ok(w, map[string]any{"outcome": "sold_out", "remaining": stock, "capacity": capacity})
		return
	}
	var product, name, sku string
	var price int
	e = tx.QueryRow("SELECT p.id,p.name,v.sku,v.price FROM shop_variants v JOIN shop_products p ON p.id=v.product_id WHERE v.id=? AND v.organization_id=? AND p.organization_id=?", variant, id.OrganizationID, id.OrganizationID).Scan(&product, &name, &sku, &price)
	if e != nil {
		fail(w, 503, "Product unavailable")
		return
	}
	orderID = token()
	orderNo := "LAB-" + orderID[:12]
	_, e = tx.Exec("INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,discount_amount,shipping_fee,total_amount,payment_status,fulfillment_status,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", orderID, id.OrganizationID, orderNo, id.UserID, "lab@example.invalid", `{"name":"Concurrency Lab","address":"Isolated fixture"}`, price, 0, 0, price, "pending", "unfulfilled", strings.TrimSpace(in.Key))
	if e == nil {
		_, e = tx.Exec("INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,1,?)", token(), id.OrganizationID, orderID, variant, name, "Lab unit", sku, price, price)
	}
	if e == nil {
		_, e = tx.Exec("INSERT INTO shop_checkout_lab_orders(run_id,request_key,order_id) VALUES(?,?,?)", in.RunID, strings.TrimSpace(in.Key), orderID)
	}
	if e != nil {
		fail(w, 409, "Checkout could not be committed")
		return
	}
	var stock int
	e = tx.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=? AND organization_id=?", variant, id.OrganizationID).Scan(&stock)
	if e != nil || tx.Commit() != nil {
		fail(w, 503, "Checkout transaction failed")
		return
	}
	ok(w, map[string]any{"outcome": "accepted", "orderId": orderID, "orderNumber": orderNo, "remaining": stock, "capacity": capacity, "unitPrice": price})
}
