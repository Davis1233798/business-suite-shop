package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type shopCommand struct {
	Action         string `json:"action"`
	VariantID      string `json:"variantId"`
	Quantity       int    `json:"quantity"`
	OrderID        string `json:"orderId"`
	Tracking       string `json:"tracking"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// expireShopReservations releases inventory for expired unpaid demo orders.
// It runs in its own transaction, so failed subsequent requests do not undo expiry.
func (a *App) expireShopReservations() error {
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	rows, e := tx.Query(`SELECT r.id,r.organization_id,r.variant_id,r.quantity,r.cart_id FROM shop_inventory_reservations r JOIN shop_orders o ON o.id=r.cart_id AND o.organization_id=r.organization_id WHERE r.status='active' AND o.payment_status IN ('pending','failed') AND EXISTS (SELECT 1 FROM (SELECT x.organization_id,x.cart_id FROM shop_inventory_reservations x JOIN shop_orders pending ON pending.id=x.cart_id AND pending.organization_id=x.organization_id WHERE x.status='active' AND pending.payment_status IN ('pending','failed') AND datetime(x.expires_at)<=CURRENT_TIMESTAMP GROUP BY x.organization_id,x.cart_id LIMIT 100) expired WHERE expired.organization_id=r.organization_id AND expired.cart_id=r.cart_id)`)
	if e != nil {
		return e
	}
	type reservation struct {
		id, org, variant, order string
		qty                     int
	}
	list := []reservation{}
	for rows.Next() {
		var x reservation
		if e = rows.Scan(&x.id, &x.org, &x.variant, &x.qty, &x.order); e != nil {
			rows.Close()
			return e
		}
		list = append(list, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, x := range list {
		claimed, claimErr := tx.Exec("UPDATE shop_inventory_reservations SET status='expired' WHERE id=? AND organization_id=? AND status='active'", x.id, x.org)
		if claimErr != nil {
			return claimErr
		}
		claimedCount, claimErr := claimed.RowsAffected()
		if claimErr != nil {
			return claimErr
		}
		if claimedCount != 1 {
			return errors.New("reservation state changed during expiry")
		}
		result, updateErr := tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity+? WHERE id=? AND organization_id=?", x.qty, x.variant, x.org)
		if updateErr != nil {
			return updateErr
		}
		count, countErr := result.RowsAffected()
		if countErr != nil {
			return countErr
		}
		if count != 1 {
			return errors.New("reservation variant not found")
		}
		if _, e = tx.Exec("UPDATE shop_orders SET payment_status='expired' WHERE id=? AND organization_id=? AND payment_status IN ('pending','failed')", x.order, x.org); e != nil {
			return e
		}
	}
	return tx.Commit()
}

// shopBusiness implements demo checkout and simulated payment in the authenticated
// station's own database. No real payment or shipping integration is invoked.
func (a *App) shopBusiness(w http.ResponseWriter, r *http.Request, id *Identity, grant *Grant) {
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
		return
	}
	if grant.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	var in shopCommand
	if body(r, &in) != nil || len(in.IdempotencyKey) < 8 || len(in.IdempotencyKey) > 128 || len(in.VariantID) > 256 || len(in.OrderID) > 256 || len(in.Tracking) > 80 {
		fail(w, 400, "Invalid commerce request")
		return
	}
	if in.Action != "checkout" && in.Action != "pay" && in.Action != "ship" {
		fail(w, 400, "Unknown commerce action")
		return
	}
	if in.Action == "ship" && (grant.Scope != "back" || strings.TrimSpace(in.Tracking) == "") {
		fail(w, 403, "Shipping requires a back-office account and tracking number")
		return
	}
	if in.Action == "checkout" && (in.VariantID == "" || in.Quantity < 1 || in.Quantity > 1000) {
		fail(w, 400, "Select a product and a valid quantity")
		return
	}
	if in.Action != "checkout" && in.OrderID == "" {
		fail(w, 400, "An order is required")
		return
	}
	if e := a.expireShopReservations(); e != nil {
		fail(w, 503, "Unable to refresh expired reservations")
		return
	}
	tx, e := a.db.BeginTx(r.Context(), nil)
	if e != nil {
		fail(w, 503, "Commerce service unavailable")
		return
	}
	defer tx.Rollback()
	endpoint := "/station/shop/" + in.Action
	raw, _ := json.Marshal(in)
	fingerprint := digest(id.UserID + "\x00" + string(raw))
	var previousHash, previousBody string
	e = tx.QueryRow("SELECT request_hash,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint=?", id.OrganizationID, in.IdempotencyKey, endpoint).Scan(&previousHash, &previousBody)
	if e == nil {
		if previousHash != fingerprint {
			fail(w, 409, "Retry key was used for a different request")
			return
		}
		var result map[string]any
		if json.Unmarshal([]byte(previousBody), &result) != nil {
			fail(w, 500, "Invalid stored result")
			return
		}
		ok(w, result)
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		fail(w, 503, "Cannot read retry state")
		return
	}
	result := map[string]any{}
	orderID := in.OrderID
	switch in.Action {
	case "checkout":
		var sku, title, productName string
		var price, stock, reserved int64
		e = tx.QueryRow(`SELECT v.sku,v.title,p.name,v.price,v.stock_quantity,v.reserved_quantity FROM shop_variants v JOIN shop_products p ON p.id=v.product_id AND p.organization_id=v.organization_id WHERE v.id=? AND v.organization_id=? AND v.is_active=1 AND p.is_active=1`, in.VariantID, id.OrganizationID).Scan(&sku, &title, &productName, &price, &stock, &reserved)
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "Active product variant not found")
			return
		}
		if e != nil {
			fail(w, 503, "Cannot read product")
			return
		}
		if price < 0 || price > 1000000000 || stock-reserved < int64(in.Quantity) {
			fail(w, 409, "Insufficient available stock")
			return
		}
		orderID = "order_" + token()
		orderNo := "ORD-" + strings.ToUpper(token()[:12])
		subtotal := price * int64(in.Quantity)
		total := subtotal + 60
		update, err := tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity-? WHERE id=? AND organization_id=? AND stock_quantity-reserved_quantity>=?", in.Quantity, in.VariantID, id.OrganizationID, in.Quantity)
		if err != nil {
			fail(w, 503, "Stock update failed")
			return
		}
		n, err := update.RowsAffected()
		if err != nil || n != 1 {
			fail(w, 409, "Stock changed; refresh and retry")
			return
		}
		_, e = tx.Exec(`INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,shipping_fee,total_amount,idempotency_key) VALUES(?,?,?,?,?,'{}',?,60,?,?)`, orderID, id.OrganizationID, orderNo, id.UserID, id.Email, subtotal, total, in.IdempotencyKey)
		if e != nil {
			fail(w, 503, "Order creation failed")
			return
		}
		_, e = tx.Exec(`INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,?,?)`, "item_"+token(), id.OrganizationID, orderID, in.VariantID, productName, title, sku, price, in.Quantity, subtotal)
		if e != nil {
			fail(w, 503, "Order item creation failed")
			return
		}
		expires := time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339)
		_, e = tx.Exec(`INSERT INTO shop_inventory_reservations(id,organization_id,variant_id,session_id,cart_id,quantity,expires_at,status) VALUES(?,?,?,?,?,?,?,'active')`, "reservation_"+token(), id.OrganizationID, in.VariantID, id.UserID, orderID, in.Quantity, expires)
		if e != nil {
			fail(w, 503, "Reservation failed")
			return
		}
		result = map[string]any{"orderId": orderID, "orderNumber": orderNo, "totalAmount": total, "paymentStatus": "pending", "expiresAt": expires}
	case "pay":
		var owner, payment, status string
		var amount int64
		e = tx.QueryRow("SELECT COALESCE(user_id,''),payment_status,fulfillment_status,total_amount FROM shop_orders WHERE id=? AND organization_id=?", orderID, id.OrganizationID).Scan(&owner, &payment, &status, &amount)
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "Order not found")
			return
		}
		if e != nil {
			fail(w, 503, "Cannot read order")
			return
		}
		if owner != id.UserID && grant.Role != "admin" {
			fail(w, 403, "Cannot pay another user's order")
			return
		}
		if payment != "pending" && payment != "failed" {
			fail(w, 409, "Order is not awaiting payment")
			return
		}
		var items, active int
		if e = tx.QueryRow("SELECT count(*) FROM shop_order_items WHERE organization_id=? AND order_id=?", id.OrganizationID, orderID).Scan(&items); e != nil {
			fail(w, 503, "Cannot validate order items")
			return
		}
		if e = tx.QueryRow("SELECT count(*) FROM shop_inventory_reservations WHERE organization_id=? AND cart_id=? AND status='active' AND datetime(expires_at)>CURRENT_TIMESTAMP", id.OrganizationID, orderID).Scan(&active); e != nil {
			fail(w, 503, "Cannot validate reservations")
			return
		}
		if items < 1 || active != items {
			fail(w, 409, "Reservations are incomplete or expired")
			return
		}
		var unmatched int
		if e = tx.QueryRow(`SELECT count(*) FROM shop_order_items i WHERE i.organization_id=? AND i.order_id=? AND NOT EXISTS (SELECT 1 FROM shop_inventory_reservations r WHERE r.organization_id=i.organization_id AND r.cart_id=i.order_id AND r.variant_id=i.variant_id AND r.quantity=i.quantity AND r.status='active' AND datetime(r.expires_at)>CURRENT_TIMESTAMP)`, id.OrganizationID, orderID).Scan(&unmatched); e != nil {
			fail(w, 503, "Cannot match reservations")
			return
		}
		if unmatched != 0 {
			fail(w, 409, "Reservations do not match the order")
			return
		}
		update, err := tx.Exec("UPDATE shop_inventory_reservations SET status='consumed' WHERE organization_id=? AND cart_id=? AND status='active' AND datetime(expires_at)>CURRENT_TIMESTAMP", id.OrganizationID, orderID)
		if err != nil {
			fail(w, 503, "Cannot consume reservation")
			return
		}
		n, err := update.RowsAffected()
		if err != nil || n != int64(items) {
			fail(w, 409, "Reservation expired or unavailable")
			return
		}
		paymentUpdate, updateErr := tx.Exec("UPDATE shop_orders SET payment_status='paid' WHERE id=? AND organization_id=? AND payment_status IN ('pending','failed')", orderID, id.OrganizationID)
		if updateErr != nil {
			fail(w, 503, "Payment update failed")
			return
		}
		updated, countErr := paymentUpdate.RowsAffected()
		if countErr != nil || updated != 1 {
			fail(w, 409, "Payment state changed; refresh and retry")
			return
		}
		if _, e = tx.Exec("INSERT INTO shop_payment_attempts(id,organization_id,order_id,attempt_reference,amount,provider,status) VALUES(?,?,?,?,?,'mock_gateway','success')", "payment_"+token(), id.OrganizationID, orderID, in.IdempotencyKey, amount); e != nil {
			fail(w, 503, "Payment record failed")
			return
		}
		event, _ := json.Marshal(map[string]string{"orderId": orderID})
		if _, e = tx.Exec("INSERT INTO outbox_events(id,organization_id,event_type,payload) VALUES(?,?,'shop.paid',?)", "shop.paid:"+orderID, id.OrganizationID, string(event)); e != nil {
			fail(w, 503, "Fulfillment event failed")
			return
		}
		result = map[string]any{"orderId": orderID, "paymentStatus": "paid", "fulfillmentStatus": status, "simulated": true}
	case "ship":
		var payment, status string
		e = tx.QueryRow("SELECT payment_status,fulfillment_status FROM shop_orders WHERE id=? AND organization_id=?", orderID, id.OrganizationID).Scan(&payment, &status)
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "Order not found")
			return
		}
		if e != nil {
			fail(w, 503, "Cannot read order")
			return
		}
		if payment != "paid" || status != "unfulfilled" {
			fail(w, 409, "Only paid, unfulfilled orders can be shipped")
			return
		}
		tracking := strings.TrimSpace(in.Tracking)
		if _, e = tx.Exec("UPDATE shop_orders SET fulfillment_status='shipped',tracking_number=? WHERE id=? AND organization_id=?", tracking, orderID, id.OrganizationID); e != nil {
			fail(w, 503, "Shipping update failed")
			return
		}
		if _, e = tx.Exec("INSERT INTO shop_shipments(id,organization_id,order_id,tracking_number,carrier,status) VALUES(?,?,?,?,'Demo carrier','shipped')", "shipment_"+token(), id.OrganizationID, orderID, tracking); e != nil {
			fail(w, 503, "Shipment record failed")
			return
		}
		result = map[string]any{"orderId": orderID, "fulfillmentStatus": "shipped", "tracking": tracking, "simulated": true}
	}
	encoded, _ := json.Marshal(result)
	if _, e = tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,200,?)", "idem_"+token(), id.OrganizationID, in.IdempotencyKey, endpoint, fingerprint, string(encoded)); e != nil {
		fail(w, 503, "Unable to save retry state")
		return
	}
	if _, e = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,after_value) VALUES(?,?,?,?,?,'shop_order',?,?)", "audit_"+token(), id.OrganizationID, id.UserID, grant.Role, "shop."+in.Action, orderID, string(encoded)); e != nil {
		fail(w, 503, "Unable to record commerce audit")
		return
	}
	if e = tx.Commit(); e != nil {
		fail(w, 503, "Commerce transaction failed")
		return
	}
	ok(w, result)
}

func (a *App) startReservationExpiry() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if e := a.expireShopReservations(); e != nil {
				fmt.Printf("reservation expiry failed: %v\n", e)
			}
		}
	}()
}
