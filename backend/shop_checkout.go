package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

var errCheckoutInvalid = errors.New("invalid checkout request")
var errCheckoutNotFound = errors.New("product variant not found")
var errCheckoutSoldOut = errors.New("insufficient inventory")
var errCheckoutIdempotency = errors.New("idempotency key was used with different checkout data")

type checkoutInput struct {
	VariantID string         `json:"variantId"`
	Quantity  int            `json:"quantity"`
	Key       string         `json:"idempotencyKey"`
	Email     string         `json:"customerEmail"`
	Address   map[string]any `json:"shippingAddress"`
}

func checkoutOrder(ctx context.Context, db *sql.DB, organizationID, userID string, in checkoutInput) (map[string]any, error) {
	in.Key = strings.TrimSpace(in.Key)
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if in.VariantID == "" || in.Quantity < 1 || in.Quantity > 100 || len(in.Key) < 1 || len(in.Key) > 128 || !strings.Contains(in.Email, "@") || len(in.Email) > 254 || len(in.Address) == 0 {
		return nil, errCheckoutInvalid
	}
	addressBytes, err := json.Marshal(in.Address)
	if err != nil || len(addressBytes) > 8192 {
		return nil, errCheckoutInvalid
	}
	requestHash := digest(in.VariantID + "\n" + strconv.Itoa(in.Quantity) + "\n" + in.Email + "\n" + string(addressBytes))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var oldHash, oldBody string
	err = tx.QueryRow("SELECT request_hash,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='shop_checkout'", organizationID, in.Key).Scan(&oldHash, &oldBody)
	if err == nil {
		if oldHash != requestHash {
			return nil, errCheckoutIdempotency
		}
		var result map[string]any
		if json.Unmarshal([]byte(oldBody), &result) != nil {
			return nil, errors.New("stored checkout response is invalid")
		}
		result["duplicate"] = true
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	var productName, title, sku string
	var price int
	err = tx.QueryRow(`SELECT p.name,v.title,v.sku,v.price FROM shop_variants v JOIN shop_products p ON p.id=v.product_id WHERE v.id=? AND v.organization_id=? AND v.is_active=1 AND p.organization_id=? AND p.is_active=1`, in.VariantID, organizationID, organizationID).Scan(&productName, &title, &sku, &price)
	if err == sql.ErrNoRows {
		return nil, errCheckoutNotFound
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity-? WHERE id=? AND organization_id=? AND is_active=1 AND stock_quantity>=?", in.Quantity, in.VariantID, organizationID, in.Quantity)
	if err != nil {
		return nil, err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, errCheckoutSoldOut
	}
	orderID := token()
	orderNo := "SHOP-" + orderID[:12]
	total := price * in.Quantity
	_, err = tx.Exec(`INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,shipping_address_json,subtotal,discount_amount,shipping_fee,total_amount,payment_status,fulfillment_status,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, orderID, organizationID, orderNo, userID, in.Email, string(addressBytes), total, 0, 0, total, "pending", "unfulfilled", in.Key)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,?,?)`, token(), organizationID, orderID, in.VariantID, productName, title, sku, price, in.Quantity, total)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"orderId": orderID, "orderNumber": orderNo, "totalAmount": total, "unitPrice": price, "quantity": in.Quantity, "paymentStatus": "pending", "fulfillmentStatus": "unfulfilled"}
	body, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,?,?,200,?)", token(), organizationID, in.Key, "shop_checkout", requestHash, string(body))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *App) shopCheckoutAPI(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("site") != "shop" {
		fail(w, 404, "Checkout is unavailable")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
		return
	}
	id, err := a.identity(r)
	if err != nil {
		fail(w, 401, "Sign in to checkout")
		return
	}
	g := id.grant("shop")
	if g == nil || g.Scope != "front" && g.Scope != "back" {
		fail(w, 403, "Shop access is required")
		return
	}
	if g.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var in checkoutInput
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 12*1024)).Decode(&in) != nil {
		fail(w, 400, "Invalid checkout request")
		return
	}
	data, err := checkoutOrder(r.Context(), a.db, id.OrganizationID, id.UserID, in)
	switch err {
	case nil:
		ok(w, data)
	case errCheckoutInvalid:
		fail(w, 400, err.Error())
	case errCheckoutNotFound:
		fail(w, 404, err.Error())
	case errCheckoutSoldOut:
		fail(w, 409, err.Error())
	case errCheckoutIdempotency:
		fail(w, 409, err.Error())
	default:
		fail(w, 503, "Checkout transaction failed")
	}
}
