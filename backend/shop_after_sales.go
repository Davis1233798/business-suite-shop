package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type afterSalesRequest struct {
	Action    string `json:"action"`
	OrderID   string `json:"orderId"`
	ItemID    string `json:"itemId"`
	ProductID string `json:"productId"`
	Quantity  int64  `json:"quantity"`
	Rating    int    `json:"rating"`
	Reason    string `json:"reason"`
	Comment   string `json:"comment"`
	Key       string `json:"idempotencyKey"`
}

func (a *App) shopAfterSales(w http.ResponseWriter, r *http.Request, id *Identity, g *Grant) {
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
		return
	}
	if g.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	var in afterSalesRequest
	if body(r, &in) != nil || len(in.Key) < 8 || len(in.Key) > 128 || len(in.OrderID) == 0 || len(in.OrderID) > 256 || len(in.ItemID) > 256 || len(in.ProductID) > 256 || (in.Action != "refund" && in.Action != "review") {
		fail(w, 400, "Invalid after-sales request")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	in.Comment = strings.TrimSpace(in.Comment)
	if in.Action == "refund" {
		if g.Role != "admin" || g.Scope != "back" {
			fail(w, 403, "Manager back-office access required")
			return
		}
		if in.ItemID == "" || in.Quantity < 1 || in.Quantity > 1000 || in.Reason == "" || len(in.Reason) > 500 {
			fail(w, 400, "Enter a valid return quantity and reason")
			return
		}
	}
	if in.Action == "review" && (in.ProductID == "" || in.Rating < 1 || in.Rating > 5 || len(in.Comment) > 1000) {
		fail(w, 400, "Enter a valid product review")
		return
	}
	tx, e := a.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 503, "Commerce unavailable")
		return
	}
	defer tx.Rollback()
	raw, _ := json.Marshal(in)
	hash := digest(id.UserID + "\x00" + string(raw))
	endpoint := "/station/shop/" + in.Action
	var oldHash, oldBody string
	e = tx.QueryRow("SELECT request_hash,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint=?", id.OrganizationID, in.Key, endpoint).Scan(&oldHash, &oldBody)
	if e == nil {
		if hash != oldHash {
			fail(w, 409, "Retry key already used")
			return
		}
		var result map[string]any
		if json.Unmarshal([]byte(oldBody), &result) != nil {
			fail(w, 500, "Invalid retry result")
			return
		}
		ok(w, result)
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		fail(w, 503, "Cannot read retry state")
		return
	}
	var owner, payment, fulfillment string
	e = tx.QueryRow("SELECT COALESCE(user_id,''),payment_status,fulfillment_status FROM shop_orders WHERE organization_id=? AND id=?", id.OrganizationID, in.OrderID).Scan(&owner, &payment, &fulfillment)
	if errors.Is(e, sql.ErrNoRows) {
		fail(w, 404, "Order not found")
		return
	}
	if e != nil {
		fail(w, 503, "Cannot read order")
		return
	}
	if payment != "paid" {
		fail(w, 409, "Order must be paid")
		return
	}
	result := map[string]any{}
	entity := ""
	if in.Action == "refund" {
		if fulfillment != "shipped" {
			fail(w, 409, "Only shipped orders can be returned")
			return
		}
		var variant string
		var quantity, lineTotal int64
		e = tx.QueryRow("SELECT variant_id,quantity,line_total FROM shop_order_items WHERE organization_id=? AND order_id=? AND id=?", id.OrganizationID, in.OrderID, in.ItemID).Scan(&variant, &quantity, &lineTotal)
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "Order item not found")
			return
		}
		if e != nil {
			fail(w, 503, "Cannot read order item")
			return
		}
		if quantity < 1 || quantity > 1000000 || lineTotal < 0 || lineTotal > 1000000000000000 {
			fail(w, 409, "Invalid order item values")
			return
		}
		var returned int64
		e = tx.QueryRow("SELECT COALESCE(SUM(quantity),0) FROM shop_returns WHERE organization_id=? AND order_item_id=? AND status='refunded'", id.OrganizationID, in.ItemID).Scan(&returned)
		if e != nil {
			fail(w, 503, "Cannot read returns")
			return
		}
		if returned < 0 || returned > quantity || in.Quantity > quantity-returned {
			fail(w, 409, "Return exceeds purchased quantity")
			return
		}
		allocate := func(n int64) int64 { return (lineTotal/quantity)*n + ((lineTotal%quantity)*n)/quantity }
		refund := allocate(returned+in.Quantity) - allocate(returned)
		entity = "return_" + token()
		_, e = tx.Exec("INSERT INTO shop_returns(id,organization_id,order_id,order_item_id,quantity,reason,status,refund_amount) VALUES(?,?,?,?,?,?,'refunded',?)", entity, id.OrganizationID, in.OrderID, in.ItemID, in.Quantity, in.Reason, refund)
		if e == nil {
			var updated sql.Result
			updated, e = tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity+? WHERE organization_id=? AND id=? AND stock_quantity>=0 AND stock_quantity<=1000000000-?", in.Quantity, id.OrganizationID, variant, in.Quantity)
			if e == nil {
				n, err := updated.RowsAffected()
				e = err
				if e == nil && n != 1 {
					fail(w, 409, "Variant inventory unavailable")
					return
				}
			}
		}
		result = map[string]any{"returnId": entity, "refundAmount": refund, "quantity": in.Quantity, "simulated": true}
	} else {
		if owner == "" || owner != id.UserID {
			fail(w, 403, "Only the purchaser may review this order")
			return
		}
		var purchased int
		e = tx.QueryRow("SELECT count(*) FROM shop_order_items i JOIN shop_variants v ON v.id=i.variant_id AND v.organization_id=i.organization_id WHERE i.organization_id=? AND i.order_id=? AND v.product_id=?", id.OrganizationID, in.OrderID, in.ProductID).Scan(&purchased)
		if e != nil {
			fail(w, 503, "Cannot validate purchase")
			return
		}
		if purchased == 0 {
			fail(w, 404, "Product not found in order")
			return
		}
		var previous int
		e = tx.QueryRow("SELECT count(*) FROM shop_reviews WHERE organization_id=? AND order_id=? AND product_id=? AND user_id=?", id.OrganizationID, in.OrderID, in.ProductID, id.UserID).Scan(&previous)
		if e != nil {
			fail(w, 503, "Cannot read reviews")
			return
		}
		if previous > 0 {
			fail(w, 409, "This product has already been reviewed for this order")
			return
		}
		entity = "review_" + token()
		_, e = tx.Exec("INSERT INTO shop_reviews(id,organization_id,product_id,user_id,order_id,rating,comment,is_visible) VALUES(?,?,?,?,?,?,?,1)", entity, id.OrganizationID, in.ProductID, id.UserID, in.OrderID, in.Rating, in.Comment)
		result = map[string]any{"reviewId": entity, "rating": in.Rating}
	}
	if e != nil {
		fail(w, 503, "Cannot save after-sales transaction")
		return
	}
	encoded, _ := json.Marshal(result)
	_, e = tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,200,?)", "idem_"+token(), id.OrganizationID, in.Key, endpoint, hash, string(encoded))
	if e == nil {
		_, e = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,after_value,reason) VALUES(?,?,?,?,?,'shop',?,?,?)", "audit_"+token(), id.OrganizationID, id.UserID, g.Role, "shop."+in.Action, entity, string(encoded), in.Reason)
	}
	if e != nil {
		fail(w, 503, "Cannot record after-sales transaction")
		return
	}
	if tx.Commit() != nil {
		fail(w, 503, "Cannot commit after-sales transaction")
		return
	}
	ok(w, result)
}
