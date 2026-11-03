package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

func releaseExpiredShopReservations(db *sql.DB, organizationID string, now time.Time) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT r.id,r.variant_id,r.quantity,r.cart_id,COALESCE(o.payment_status,'') FROM shop_inventory_reservations r LEFT JOIN shop_orders o ON o.id=r.cart_id AND o.organization_id=r.organization_id WHERE r.organization_id=? AND r.session_id LIKE 'shop-go-checkout:%' AND r.status='active' AND datetime(r.expires_at)<=datetime(?)`, organizationID, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type expiredReservation struct {
		id, variant, orderID, payment string
		quantity                      int
	}
	expired := []expiredReservation{}
	for rows.Next() {
		var item expiredReservation
		if err = rows.Scan(&item.id, &item.variant, &item.quantity, &item.orderID, &item.payment); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, item := range expired {
		status := "expired"
		if item.payment == "paid" {
			status = "consumed"
		}
		res, e := tx.Exec("UPDATE shop_inventory_reservations SET status=? WHERE id=? AND organization_id=? AND status='active'", status, item.id, organizationID)
		if e != nil {
			return e
		}
		changed, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if changed != 1 {
			continue
		}
		if item.payment == "pending" || item.payment == "failed" {
			if _, e = tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity+? WHERE id=? AND organization_id=?", item.quantity, item.variant, organizationID); e != nil {
				return e
			}
			if _, e = tx.Exec("UPDATE shop_orders SET payment_status='expired' WHERE id=? AND organization_id=? AND payment_status IN ('pending','failed')", item.orderID, organizationID); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}

var errCheckoutInvalid = errors.New("invalid checkout request")
var errCheckoutNotFound = errors.New("product variant not found")
var errCheckoutSoldOut = errors.New("insufficient inventory")
var errCheckoutIdempotency = errors.New("idempotency key was used with different checkout data")
var errCheckoutExpired = errors.New("checkout expired; submit a new checkout with a new idempotency key")
var errCheckoutCoupon = errors.New("coupon is invalid, expired, inactive, or below its minimum spend")
var checkoutEmailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type checkoutItemInput struct {
	VariantID string `json:"variantId"`
	Quantity  int    `json:"quantity"`
}

type checkoutInput struct {
	VariantID  string              `json:"variantId"`
	Quantity   int                 `json:"quantity"`
	Items      []checkoutItemInput `json:"items"`
	CouponCode string              `json:"couponCode"`
	Key        string              `json:"idempotencyKey"`
	Email      string              `json:"customerEmail"`
	Address    map[string]any      `json:"shippingAddress"`
}

type checkoutResolvedLine struct {
	variantID, productName, title, sku string
	quantity                           int
	unitPrice, rawLineTotal, lineTotal int64
}

func parseShopCouponExpiry(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999", "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, errors.New("invalid coupon expiry")
}

func shopCouponDiscount(tx *sql.Tx, organizationID, code string, subtotal int64, now time.Time) (int64, error) {
	if code == "" {
		return 0, nil
	}
	var kind string
	var value, minSpend int64
	var expires sql.NullString
	var active int
	err := tx.QueryRow("SELECT discount_type,discount_value,min_spend,expires_at,is_active FROM shop_promotions WHERE organization_id=? AND code=?", organizationID, code).Scan(&kind, &value, &minSpend, &expires, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errCheckoutCoupon
	}
	if err != nil {
		return 0, err
	}
	if active != 1 || minSpend < 0 || subtotal < minSpend || value < 0 {
		return 0, errCheckoutCoupon
	}
	if expires.Valid && strings.TrimSpace(expires.String) != "" {
		deadline, parseErr := parseShopCouponExpiry(strings.TrimSpace(expires.String))
		if parseErr != nil || !deadline.After(now) {
			return 0, errCheckoutCoupon
		}
	}
	var discount int64
	switch kind {
	case "percentage":
		var product, round big.Int
		product.Mul(big.NewInt(subtotal), big.NewInt(value))
		round.Add(&product, big.NewInt(50)) // legacy Math.round for non-negative currency values
		round.Quo(&round, big.NewInt(100))
		if round.Cmp(big.NewInt(subtotal)) > 0 {
			discount = subtotal
		} else {
			discount = round.Int64()
		}
	case "fixed":
		discount = value
	default:
		return 0, errCheckoutCoupon
	}
	if discount > subtotal {
		discount = subtotal
	}
	return discount, nil
}

func allocateShopCouponDiscount(lines []checkoutResolvedLine, subtotal, discount int64) error {
	if subtotal < 0 || discount < 0 || discount > subtotal {
		return errCheckoutCoupon
	}
	if subtotal == 0 {
		return nil
	}
	allocated := int64(0)
	for i := range lines {
		if lines[i].rawLineTotal < 0 {
			return errCheckoutInvalid
		}
		var product big.Int
		product.Mul(big.NewInt(lines[i].rawLineTotal), big.NewInt(discount))
		product.Quo(&product, big.NewInt(subtotal))
		share := product.Int64()
		if share > lines[i].rawLineTotal || allocated > discount-share {
			return errCheckoutInvalid
		}
		allocated += share
		lines[i].lineTotal = lines[i].rawLineTotal - share
	}
	remaining := discount - allocated
	for i := len(lines) - 1; i >= 0 && remaining > 0; i-- {
		extra := lines[i].lineTotal
		if extra > remaining {
			extra = remaining
		}
		lines[i].lineTotal -= extra
		remaining -= extra
	}
	if remaining != 0 {
		return errCheckoutInvalid
	}
	return nil
}

func checkoutOrder(ctx context.Context, db *sql.DB, organizationID, userID string, in checkoutInput) (map[string]any, error) {
	if err := releaseExpiredShopReservations(db, organizationID, time.Now()); err != nil {
		return nil, err
	}
	in.Key = strings.TrimSpace(in.Key)
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	in.CouponCode = strings.TrimSpace(in.CouponCode)
	legacySingleLine := len(in.Items) == 0 && in.VariantID != ""
	if legacySingleLine && (in.Quantity < 1 || in.Quantity > 100) {
		return nil, errCheckoutInvalid
	}
	if legacySingleLine {
		in.Items = []checkoutItemInput{{VariantID: in.VariantID, Quantity: in.Quantity}}
	} else if len(in.Items) > 0 && (in.VariantID != "" || in.Quantity != 0) {
		return nil, errCheckoutInvalid
	}
	if len(in.Items) < 1 || len(in.Items) > 50 || len(in.Key) < 1 || len(in.Key) > 128 || len(in.CouponCode) > 80 || !checkoutEmailPattern.MatchString(in.Email) || len(in.Email) > 254 || len(in.Address) == 0 {
		return nil, errCheckoutInvalid
	}
	for _, field := range []string{"name", "city", "address", "phone"} {
		value, ok := in.Address[field].(string)
		if !ok || strings.TrimSpace(value) == "" || len(value) > 300 {
			return nil, errCheckoutInvalid
		}
	}
	addressBytes, err := json.Marshal(in.Address)
	if err != nil || len(addressBytes) > 8192 {
		return nil, errCheckoutInvalid
	}
	quantities := make(map[string]int, len(in.Items))
	for _, item := range in.Items {
		if !paymentReferencePattern.MatchString(item.VariantID) || item.Quantity < 1 || item.Quantity > 999 || quantities[item.VariantID] > 999-item.Quantity {
			return nil, errCheckoutInvalid
		}
		quantities[item.VariantID] += item.Quantity
	}
	canonicalItems := make([]checkoutItemInput, 0, len(quantities))
	for variantID, quantity := range quantities {
		canonicalItems = append(canonicalItems, checkoutItemInput{VariantID: variantID, Quantity: quantity})
	}
	sort.Slice(canonicalItems, func(i, j int) bool { return canonicalItems[i].VariantID < canonicalItems[j].VariantID })
	requestPayload, err := json.Marshal(struct {
		UserID, Email, Coupon string
		Items                 []checkoutItemInput
		Address               json.RawMessage
	}{userID, in.Email, in.CouponCode, canonicalItems, addressBytes})
	if err != nil {
		return nil, errCheckoutInvalid
	}
	requestHash := digest(string(requestPayload))
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var oldHash, oldBody string
	err = tx.QueryRow("SELECT request_hash,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='shop_checkout'", organizationID, in.Key).Scan(&oldHash, &oldBody)
	if err == nil {
		legacyHashMatches := false
		if legacySingleLine && in.CouponCode == "" {
			legacyHash := digest(userID + "\n" + in.VariantID + "\n" + strconv.Itoa(in.Quantity) + "\n" + in.Email + "\n" + string(addressBytes))
			legacyHashMatches = oldHash == legacyHash
		}
		if oldHash != requestHash && !legacyHashMatches {
			return nil, errCheckoutIdempotency
		}
		var result map[string]any
		if json.Unmarshal([]byte(oldBody), &result) != nil {
			return nil, errors.New("stored checkout response is invalid")
		}
		orderID, ok := result["orderId"].(string)
		if !ok {
			return nil, errors.New("stored checkout order is invalid")
		}
		var paymentStatus string
		if err = tx.QueryRow("SELECT payment_status FROM shop_orders WHERE id=? AND organization_id=?", orderID, organizationID).Scan(&paymentStatus); err != nil {
			return nil, err
		}
		if paymentStatus == "expired" {
			return nil, errCheckoutExpired
		}
		result["paymentStatus"] = paymentStatus
		result["duplicate"] = true
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return result, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	lines := make([]checkoutResolvedLine, 0, len(canonicalItems))
	var subtotal int64
	for _, item := range canonicalItems {
		line := checkoutResolvedLine{variantID: item.VariantID, quantity: item.Quantity}
		err = tx.QueryRow(`SELECT p.name,v.title,v.sku,v.price FROM shop_variants v JOIN shop_products p ON p.id=v.product_id WHERE v.id=? AND v.organization_id=? AND v.is_active=1 AND p.organization_id=? AND p.is_active=1`, item.VariantID, organizationID, organizationID).Scan(&line.productName, &line.title, &line.sku, &line.unitPrice)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errCheckoutNotFound
		}
		if err != nil {
			return nil, err
		}
		if line.unitPrice < 0 || line.unitPrice > (1<<63-1)/int64(item.Quantity) {
			return nil, errCheckoutInvalid
		}
		line.rawLineTotal = line.unitPrice * int64(item.Quantity)
		if subtotal > (1<<63-1)-line.rawLineTotal {
			return nil, errCheckoutInvalid
		}
		subtotal += line.rawLineTotal
		lines = append(lines, line)
	}
	couponDiscount, err := shopCouponDiscount(tx, organizationID, in.CouponCode, subtotal, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	netSubtotal := subtotal - couponDiscount
	shippingFee := int64(60) // legacy Shop pricing is fixed NT$60; it has no free-shipping threshold.
	if netSubtotal > (1<<63-1)-shippingFee {
		return nil, errCheckoutInvalid
	}
	total := netSubtotal + shippingFee
	if err = allocateShopCouponDiscount(lines, subtotal, couponDiscount); err != nil {
		return nil, err
	}
	for _, line := range lines {
		res, updateErr := tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity-? WHERE id=? AND organization_id=? AND is_active=1 AND stock_quantity-reserved_quantity>=?", line.quantity, line.variantID, organizationID, line.quantity)
		if updateErr != nil {
			return nil, updateErr
		}
		changed, updateErr := res.RowsAffected()
		if updateErr != nil {
			return nil, updateErr
		}
		if changed != 1 {
			return nil, errCheckoutSoldOut
		}
	}
	orderID := token()
	orderNo := "SHOP-" + orderID[:12]
	_, err = tx.Exec(`INSERT INTO shop_orders(id,organization_id,order_number,user_id,customer_email,coupon_code,shipping_address_json,subtotal,discount_amount,shipping_fee,total_amount,payment_status,fulfillment_status,idempotency_key) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, orderID, organizationID, orderNo, userID, in.Email, nullableCoupon(in.CouponCode), string(addressBytes), subtotal, couponDiscount, shippingFee, total, "pending", "unfulfilled", in.Key)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		if _, err = tx.Exec(`INSERT INTO shop_order_items(id,organization_id,order_id,variant_id,product_name,variant_title,sku,unit_price,quantity,line_total) VALUES(?,?,?,?,?,?,?,?,?,?)`, token(), organizationID, orderID, line.variantID, line.productName, line.title, line.sku, line.unitPrice, line.quantity, line.lineTotal); err != nil {
			return nil, err
		}
		if _, err = tx.Exec("INSERT INTO shop_inventory_reservations(id,organization_id,variant_id,session_id,cart_id,quantity,expires_at,status) VALUES(?,?,?,?,?,?,?,'active')", token(), organizationID, line.variantID, "shop-go-checkout:"+userID, orderID, line.quantity, time.Now().UTC().Add(15*time.Minute).Format(time.RFC3339Nano)); err != nil {
			return nil, err
		}
	}
	result := map[string]any{"orderId": orderID, "orderNumber": orderNo, "subtotal": subtotal, "discountAmount": couponDiscount, "couponCode": in.CouponCode, "shippingFee": shippingFee, "totalAmount": total, "paymentStatus": "pending", "fulfillmentStatus": "unfulfilled", "duplicate": false}
	if len(lines) == 1 {
		result["unitPrice"] = lines[0].unitPrice
		result["quantity"] = lines[0].quantity
	}
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

func nullableCoupon(code string) any {
	if code == "" {
		return nil
	}
	return code
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
	case errCheckoutExpired:
		fail(w, 409, err.Error())
	case errCheckoutCoupon:
		fail(w, 409, err.Error())
	default:
		fail(w, 503, "Checkout transaction failed")
	}
}
