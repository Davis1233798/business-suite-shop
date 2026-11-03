package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"unicode"
)

var (
	errShopRefundInvalid  = errors.New("invalid refund request")
	errShopRefundNotFound = errors.New("order item not found")
	errShopRefundConflict = errors.New("order or return quantity conflicts with this refund")
)

type shopRefundInput struct {
	OrderID     string `json:"orderId"`
	OrderItemID string `json:"orderItemId"`
	Quantity    int    `json:"quantity"`
	Reason      string `json:"reason"`
	Key         string `json:"idempotencyKey"`
}

type shopRefundResult struct {
	OrderID              string `json:"orderId"`
	OrderItemID          string `json:"orderItemId"`
	RefundAmount         int64  `json:"refundAmount"`
	RefundedQuantity     int64  `json:"refundedQty"`
	ItemRefundedQuantity int64  `json:"itemRefundedQuantity"`
	ItemRefundedAmount   int64  `json:"itemRefundedAmount"`
	OrderRefundedAmount  int64  `json:"orderRefundedAmount"`
	PaymentStatus        string `json:"paymentStatus"`
	FulfillmentStatus    string `json:"fulfillmentStatus"`
	TrackingNumber       string `json:"trackingNumber,omitempty"`
	Simulated            bool   `json:"simulated"`
	Duplicate            bool   `json:"duplicate"`
}

func floorProportionalRefund(lineTotal, quantity, originalQuantity int64) int64 {
	var product big.Int
	product.Mul(big.NewInt(lineTotal), big.NewInt(quantity))
	product.Quo(&product, big.NewInt(originalQuantity))
	return product.Int64()
}

func createShopRefund(db *sql.DB, organizationID, actorID, actorRole string, in shopRefundInput) (shopRefundResult, error) {
	in.OrderID = strings.TrimSpace(in.OrderID)
	in.OrderItemID = strings.TrimSpace(in.OrderItemID)
	in.Reason = strings.TrimSpace(in.Reason)
	in.Key = strings.TrimSpace(in.Key)
	if !paymentReferencePattern.MatchString(in.OrderID) || !paymentReferencePattern.MatchString(in.OrderItemID) || in.Quantity < 1 || in.Quantity > 1000000 || len(in.Reason) == 0 || len(in.Reason) > 500 || strings.IndexFunc(in.Reason, unicode.IsControl) >= 0 || !paymentReferencePattern.MatchString(in.Key) {
		return shopRefundResult{}, errShopRefundInvalid
	}

	tx, err := db.Begin()
	if err != nil {
		return shopRefundResult{}, err
	}
	defer tx.Rollback()

	var paymentStatus, fulfillmentStatus, trackingNumber string
	err = tx.QueryRow("SELECT payment_status,fulfillment_status,COALESCE(tracking_number,'') FROM shop_orders WHERE organization_id=? AND id=?", organizationID, in.OrderID).Scan(&paymentStatus, &fulfillmentStatus, &trackingNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return shopRefundResult{}, errShopRefundNotFound
	}
	if err != nil {
		return shopRefundResult{}, err
	}
	originalFulfillmentStatus := fulfillmentStatus
	var itemOrderID, variantID string
	var originalQuantity, lineTotal int64
	err = tx.QueryRow("SELECT order_id,variant_id,quantity,line_total FROM shop_order_items WHERE organization_id=? AND id=?", organizationID, in.OrderItemID).Scan(&itemOrderID, &variantID, &originalQuantity, &lineTotal)
	if errors.Is(err, sql.ErrNoRows) {
		return shopRefundResult{}, errShopRefundNotFound
	}
	if err != nil {
		return shopRefundResult{}, err
	}
	if itemOrderID != in.OrderID || originalQuantity < 1 || lineTotal < 0 {
		return shopRefundResult{}, errShopRefundConflict
	}

	fingerprintPayload, _ := json.Marshal(struct {
		ActorID   string `json:"actorId"`
		ActorRole string `json:"actorRole"`
		OrderID   string `json:"orderId"`
		OrderItem string `json:"orderItemId"`
		Quantity  int    `json:"quantity"`
		Reason    string `json:"reason"`
	}{actorID, actorRole, in.OrderID, in.OrderItemID, in.Quantity, in.Reason})
	fingerprint := digest(string(fingerprintPayload))
	var previousHash, previousBody string
	err = tx.QueryRow("SELECT request_hash,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='shop_refund'", organizationID, in.Key).Scan(&previousHash, &previousBody)
	if err == nil {
		if previousHash != fingerprint {
			return shopRefundResult{}, errShopRefundConflict
		}
		var result shopRefundResult
		if json.Unmarshal([]byte(previousBody), &result) != nil {
			return shopRefundResult{}, errors.New("stored refund response is invalid")
		}
		result.Duplicate = true
		if err = tx.Commit(); err != nil {
			return shopRefundResult{}, err
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return shopRefundResult{}, err
	}
	if paymentStatus != "paid" {
		return shopRefundResult{}, errShopRefundConflict
	}
	var alreadyReturned int64
	if err = tx.QueryRow("SELECT COALESCE(SUM(quantity),0) FROM shop_returns WHERE organization_id=? AND order_item_id=?", organizationID, in.OrderItemID).Scan(&alreadyReturned); err != nil {
		return shopRefundResult{}, err
	}
	requestedQuantity := int64(in.Quantity)
	if alreadyReturned < 0 || alreadyReturned > originalQuantity || requestedQuantity > originalQuantity-alreadyReturned {
		return shopRefundResult{}, errShopRefundConflict
	}
	cumulativeQuantity := alreadyReturned + requestedQuantity
	refundAmount := floorProportionalRefund(lineTotal, cumulativeQuantity, originalQuantity) - floorProportionalRefund(lineTotal, alreadyReturned, originalQuantity)
	if refundAmount < 0 {
		return shopRefundResult{}, errShopRefundConflict
	}

	guard, err := tx.Exec("UPDATE shop_order_items SET quantity=quantity WHERE organization_id=? AND id=? AND order_id=? AND quantity>=?", organizationID, in.OrderItemID, in.OrderID, cumulativeQuantity)
	if err != nil {
		return shopRefundResult{}, err
	}
	guarded, err := guard.RowsAffected()
	if err != nil {
		return shopRefundResult{}, err
	}
	if guarded != 1 {
		return shopRefundResult{}, errShopRefundConflict
	}
	stockLimit := int64(1<<63-1) - requestedQuantity
	stock, err := tx.Exec("UPDATE shop_variants SET stock_quantity=stock_quantity+? WHERE organization_id=? AND id=? AND stock_quantity<=?", requestedQuantity, organizationID, variantID, stockLimit)
	if err != nil {
		return shopRefundResult{}, err
	}
	stockChanged, err := stock.RowsAffected()
	if err != nil {
		return shopRefundResult{}, err
	}
	if stockChanged != 1 {
		return shopRefundResult{}, errShopRefundConflict
	}
	returnID := "return_" + digest(organizationID + "\x00" + in.Key)[:32]
	if _, err = tx.Exec("INSERT INTO shop_returns(id,organization_id,order_id,order_item_id,quantity,reason,status,refund_amount) VALUES(?,?,?,?,?,?,'refunded',?)", returnID, organizationID, in.OrderID, in.OrderItemID, requestedQuantity, in.Reason, refundAmount); err != nil {
		return shopRefundResult{}, err
	}
	var itemRefundedAmount, orderRefundedAmount int64
	if err = tx.QueryRow("SELECT COALESCE(SUM(refund_amount),0) FROM shop_returns WHERE organization_id=? AND order_item_id=? AND status='refunded'", organizationID, in.OrderItemID).Scan(&itemRefundedAmount); err != nil {
		return shopRefundResult{}, err
	}
	if err = tx.QueryRow("SELECT COALESCE(SUM(refund_amount),0) FROM shop_returns WHERE organization_id=? AND order_id=? AND status='refunded'", organizationID, in.OrderID).Scan(&orderRefundedAmount); err != nil {
		return shopRefundResult{}, err
	}
	if fulfillmentStatus == "unfulfilled" || fulfillmentStatus == "return_hold" {
		var remainingLines int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM shop_order_items i WHERE i.organization_id=? AND i.order_id=? AND i.quantity>COALESCE((SELECT SUM(r.quantity) FROM shop_returns r WHERE r.organization_id=i.organization_id AND r.order_item_id=i.id),0)`, organizationID, in.OrderID).Scan(&remainingLines); err != nil {
			return shopRefundResult{}, err
		}
		nextFulfillmentStatus := "return_hold"
		if remainingLines == 0 {
			nextFulfillmentStatus = "cancelled"
		}
		update, updateErr := tx.Exec("UPDATE shop_orders SET fulfillment_status=? WHERE organization_id=? AND id=? AND payment_status='paid' AND fulfillment_status=?", nextFulfillmentStatus, organizationID, in.OrderID, fulfillmentStatus)
		if updateErr != nil {
			return shopRefundResult{}, updateErr
		}
		changed, countErr := update.RowsAffected()
		if countErr != nil {
			return shopRefundResult{}, countErr
		}
		if changed != 1 {
			return shopRefundResult{}, errShopRefundConflict
		}
		fulfillmentStatus = nextFulfillmentStatus
	}
	result := shopRefundResult{OrderID: in.OrderID, OrderItemID: in.OrderItemID, RefundAmount: refundAmount, RefundedQuantity: requestedQuantity, ItemRefundedQuantity: cumulativeQuantity, ItemRefundedAmount: itemRefundedAmount, OrderRefundedAmount: orderRefundedAmount, PaymentStatus: paymentStatus, FulfillmentStatus: fulfillmentStatus, TrackingNumber: trackingNumber, Simulated: true}
	responseBody, err := json.Marshal(result)
	if err != nil {
		return shopRefundResult{}, err
	}
	if _, err = tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,'shop_refund',?,200,?)", token(), organizationID, in.Key, fingerprint, string(responseBody)); err != nil {
		return shopRefundResult{}, err
	}
	before, _ := json.Marshal(map[string]any{"paymentStatus": paymentStatus, "fulfillmentStatus": originalFulfillmentStatus, "trackingNumber": trackingNumber, "itemRefundedQuantity": alreadyReturned})
	after, _ := json.Marshal(result)
	if _, err = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,before_value,after_value,reason) VALUES(?,?,?,?,?,'shop_order',?,?,?,?)", token(), organizationID, actorID, actorRole, "shop.refund.create", in.OrderID, string(before), string(after), in.Reason); err != nil {
		return shopRefundResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return shopRefundResult{}, err
	}
	return result, nil
}

func (a *App) shopRefundAPI(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("site") != "shop" {
		fail(w, 404, "Workspace not found")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
		return
	}
	id, err := a.identity(r)
	if err != nil {
		fail(w, 401, "Sign in to Shop")
		return
	}
	grant := id.grant("shop")
	if grant == nil || grant.Scope != "back" || grant.Role != "admin" {
		fail(w, 403, "Shop back-office administrator access is required")
		return
	}
	var in shopRefundInput
	if body(r, &in) != nil {
		fail(w, 400, errShopRefundInvalid.Error())
		return
	}
	result, err := createShopRefund(a.db, id.OrganizationID, id.UserID, grant.Role, in)
	switch {
	case err == nil:
		ok(w, result)
	case errors.Is(err, errShopRefundInvalid):
		fail(w, 400, err.Error())
	case errors.Is(err, errShopRefundNotFound):
		fail(w, 404, err.Error())
	case errors.Is(err, errShopRefundConflict):
		fail(w, 409, err.Error())
	default:
		fail(w, 503, "Refund transaction failed")
	}
}
