package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"
)

type shopCartLine struct {
	VariantID string `json:"variantId"`
	Quantity  int64  `json:"quantity"`
}
type shopAddress struct {
	Name    string `json:"name"`
	City    string `json:"city"`
	Address string `json:"address"`
	Phone   string `json:"phone"`
}
type shopCartRequest struct {
	Lines           []shopCartLine `json:"lines"`
	CouponCode      string         `json:"couponCode"`
	ShippingAddress *shopAddress   `json:"shippingAddress"`
	Key             string         `json:"idempotencyKey"`
}

func (a *App) shopCartCheckout(w http.ResponseWriter, r *http.Request, id *Identity, g *Grant) {
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
		return
	}
	if g.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	var in shopCartRequest
	if body(r, &in) != nil || len(in.Key) < 8 || len(in.Key) > 128 || len(in.Lines) == 0 || len(in.Lines) > 100 {
		fail(w, 400, "Enter a valid cart")
		return
	}
	in.CouponCode = strings.TrimSpace(in.CouponCode)
	if in.ShippingAddress != nil {
		address := in.ShippingAddress
		address.Name = strings.TrimSpace(address.Name)
		address.City = strings.TrimSpace(address.City)
		address.Address = strings.TrimSpace(address.Address)
		address.Phone = strings.TrimSpace(address.Phone)
		if address.Name == "" || address.City == "" || address.Address == "" || address.Phone == "" || len(address.Name) > 150 || len(address.City) > 100 || len(address.Address) > 300 || len(address.Phone) > 40 {
			fail(w, 400, "Enter complete shipping details")
			return
		}
	}
	if len(in.CouponCode) > 100 {
		fail(w, 400, "Invalid coupon code")
		return
	}
	seen := map[string]bool{}
	for _, l := range in.Lines {
		if l.VariantID == "" || len(l.VariantID) > 256 || l.Quantity < 1 || l.Quantity > 1000 || seen[l.VariantID] {
			fail(w, 400, "Invalid or duplicate cart variant")
			return
		}
		seen[l.VariantID] = true
	}
	if e := a.expireShopReservations(); e != nil {
		fail(w, 503, "Unable to refresh expired reservations")
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
	const endpoint = "/station/shop/cart-checkout"
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
	type resolved struct {
		shopCartLine
		sku, title, name string
		price            int64
	}
	lines := []resolved{}
	var subtotal int64
	for _, l := range in.Lines {
		var sku, title, name string
		var price, stock, reserved int64
		e = tx.QueryRow("SELECT v.sku,v.title,p.name,v.price,v.stock_quantity,v.reserved_quantity FROM shop_variants v JOIN shop_products p ON p.id=v.product_id AND p.organization_id=v.organization_id WHERE v.organization_id=? AND v.id=? AND v.is_active=1 AND p.is_active=1", id.OrganizationID, l.VariantID).Scan(&sku, &title, &name, &price, &stock, &reserved)
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "Active product variant not found")
			return
		}
		if e != nil {
			fail(w, 503, "Cannot read product")
			return
		}
		if price < 0 || price > 1000000000 || stock < 0 || reserved < 0 || stock-reserved < l.Quantity {
			fail(w, 409, "Insufficient available stock")
			return
		}
		value := price * l.Quantity
		if subtotal > 1000000000000000-value {
			fail(w, 400, "Cart total exceeds limit")
			return
		}
		subtotal += value
		lines = append(lines, resolved{l, sku, title, name, price})
	}
	order := "order_" + token()
	number := "ORD-" + strings.ToUpper(token()[:12])
	var discount int64
	if in.CouponCode != "" {
		var kind string
		var value, minSpend int64
		var enabled, validTime int
		e = tx.QueryRow("SELECT discount_type,discount_value,min_spend,is_active,CASE WHEN expires_at IS NULL THEN 1 WHEN julianday(expires_at)>julianday('now') THEN 1 ELSE 0 END FROM shop_promotions WHERE organization_id=? AND code=?", id.OrganizationID, in.CouponCode).Scan(&kind, &value, &minSpend, &enabled, &validTime)
		if errors.Is(e, sql.ErrNoRows) {
			fail(w, 404, "Coupon not found")
			return
		}
		if e != nil {
			fail(w, 503, "Cannot read coupon")
			return
		}
		if enabled != 1 || validTime != 1 || minSpend < 0 || subtotal < minSpend || value < 0 || value > 1000000000000000 {
			fail(w, 409, "Coupon is unavailable or minimum spend is not met")
			return
		}
		if kind == "percentage" {
			if value > 100 {
				fail(w, 409, "Invalid coupon percentage")
				return
			}
			discount = (subtotal/100)*value + ((subtotal%100)*value+50)/100
		} else if kind == "fixed" {
			discount = value
		} else {
			fail(w, 409, "Invalid coupon type")
			return
		}
		if discount > subtotal {
			discount = subtotal
		}
	}
	total := subtotal - discount + 60
	expires := time.Now().UTC().Add(15 * time.Minute).Format(time.RFC3339)
	addressJSON := []byte("{}")
	if in.ShippingAddress != nil {
		addressJSON, _ = json.Marshal(in.ShippingAddress)
	}
	_, e = tx.Exec("INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,discount_amount,shipping_fee,total_amount,idempotency_key) VALUES(?,?,?,?,?,?,?,?,60,?,?)", order, id.OrganizationID, number, id.UserID, id.Email, string(addressJSON), subtotal, discount, total, in.Key)
	var cumulative, allocated int64
	for _, l := range lines {
		if e != nil {
			break
		}
		var update sql.Result
		update, e = tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity-? WHERE organization_id=? AND id=? AND stock_quantity-reserved_quantity>=?", l.Quantity, id.OrganizationID, l.VariantID, l.Quantity)
		if e == nil {
			var n int64
			n, e = update.RowsAffected()
			if e == nil && n != 1 {
				fail(w, 409, "Stock changed; refresh and retry")
				return
			}
		}
		if e == nil {
			gross := l.price * l.Quantity
			cumulative += gross
			share := int64(0)
			if subtotal > 0 && discount > 0 {
				n := new(big.Int).Mul(big.NewInt(discount), big.NewInt(cumulative))
				n.Div(n, big.NewInt(subtotal))
				current := n.Int64()
				share = current - allocated
				allocated = current
			}
			_, e = tx.Exec("INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,?,?)", "item_"+token(), id.OrganizationID, order, l.VariantID, l.name, l.title, l.sku, l.price, l.Quantity, gross-share)
		}
		if e == nil {
			_, e = tx.Exec("INSERT INTO shop_inventory_reservations(id,organization_id,variant_id,session_id,cart_id,quantity,expires_at,status) VALUES(?,?,?,?,?,?,?,'active')", "reservation_"+token(), id.OrganizationID, l.VariantID, id.UserID, order, l.Quantity, expires)
		}
	}
	result := map[string]any{"orderId": order, "orderNumber": number, "totalAmount": total, "discountAmount": discount, "paymentStatus": "pending", "expiresAt": expires}
	encoded, _ := json.Marshal(result)
	if e == nil {
		_, e = tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,200,?)", "idem_"+token(), id.OrganizationID, in.Key, endpoint, hash, string(encoded))
	}
	if e == nil {
		_, e = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,after_value) VALUES(?,?,?,?,'shop.cart_checkout','shop_order',?,?)", "audit_"+token(), id.OrganizationID, id.UserID, g.Role, order, string(encoded))
	}
	if e != nil {
		fail(w, 503, "Cannot save cart order")
		return
	}
	if tx.Commit() != nil {
		fail(w, 503, "Cannot commit cart order")
		return
	}
	ok(w, result)
}
