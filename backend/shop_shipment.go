package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode"
)

var shipmentTrackingPattern = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,80}$`)

var (
	errShipmentInvalid  = errors.New("invalid shipment request")
	errShipmentNotFound = errors.New("order not found")
	errShipmentConflict = errors.New("order is not eligible for shipment or retry conflicts")
)

type shopShipmentInput struct {
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
	Carrier        string `json:"carrier"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type shopShipmentResult struct {
	OrderID        string `json:"orderId"`
	TrackingNumber string `json:"trackingNumber"`
	Carrier        string `json:"carrier"`
	Status         string `json:"status"`
	Simulated      bool   `json:"simulated"`
	Duplicate      bool   `json:"duplicate"`
}

func createShopShipment(db *sql.DB, organizationID, actorID, actorRole string, in shopShipmentInput) (shopShipmentResult, error) {
	in.OrderID = strings.TrimSpace(in.OrderID)
	in.TrackingNumber = strings.TrimSpace(in.TrackingNumber)
	in.Carrier = strings.TrimSpace(in.Carrier)
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	if in.Carrier == "" {
		in.Carrier = "Standard Logistics (simulated)"
	}
	if !paymentReferencePattern.MatchString(in.OrderID) || !shipmentTrackingPattern.MatchString(in.TrackingNumber) || len(in.Carrier) > 80 || strings.IndexFunc(in.Carrier, unicode.IsControl) >= 0 || !paymentReferencePattern.MatchString(in.IdempotencyKey) {
		return shopShipmentResult{}, errShipmentInvalid
	}

	tx, err := db.Begin()
	if err != nil {
		return shopShipmentResult{}, err
	}
	defer tx.Rollback()

	var paymentStatus, fulfillmentStatus string
	err = tx.QueryRow("SELECT payment_status,fulfillment_status FROM shop_orders WHERE id=? AND organization_id=?", in.OrderID, organizationID).Scan(&paymentStatus, &fulfillmentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return shopShipmentResult{}, errShipmentNotFound
	}
	if err != nil {
		return shopShipmentResult{}, err
	}

	fingerprintInput, _ := json.Marshal(struct {
		ActorID        string `json:"actorId"`
		ActorRole      string `json:"actorRole"`
		OrderID        string `json:"orderId"`
		TrackingNumber string `json:"trackingNumber"`
		Carrier        string `json:"carrier"`
	}{actorID, actorRole, in.OrderID, in.TrackingNumber, in.Carrier})
	fingerprint := digest(string(fingerprintInput))
	var priorHash, priorBody string
	err = tx.QueryRow("SELECT request_hash,response_body FROM idempotency_keys WHERE organization_id=? AND key=? AND endpoint='shop_shipment'", organizationID, in.IdempotencyKey).Scan(&priorHash, &priorBody)
	if err == nil {
		if priorHash != fingerprint {
			return shopShipmentResult{}, errShipmentConflict
		}
		var result shopShipmentResult
		if json.Unmarshal([]byte(priorBody), &result) != nil {
			return shopShipmentResult{}, errors.New("stored shipment response is invalid")
		}
		result.Duplicate = true
		if err = tx.Commit(); err != nil {
			return shopShipmentResult{}, err
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return shopShipmentResult{}, err
	}
	if paymentStatus != "paid" || fulfillmentStatus != "unfulfilled" {
		return shopShipmentResult{}, errShipmentConflict
	}
	var existing int
	if err = tx.QueryRow("SELECT count(*) FROM shop_shipments WHERE organization_id=? AND order_id=?", organizationID, in.OrderID).Scan(&existing); err != nil {
		return shopShipmentResult{}, err
	}
	if existing != 0 {
		return shopShipmentResult{}, errShipmentConflict
	}

	update, err := tx.Exec("UPDATE shop_orders SET fulfillment_status='shipped',tracking_number=? WHERE id=? AND organization_id=? AND payment_status='paid' AND fulfillment_status='unfulfilled'", in.TrackingNumber, in.OrderID, organizationID)
	if err != nil {
		return shopShipmentResult{}, err
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return shopShipmentResult{}, err
	}
	if changed != 1 {
		return shopShipmentResult{}, errShipmentConflict
	}
	shipmentID := "shipment_" + digest(organizationID + "\x00" + in.OrderID)[:32]
	if _, err = tx.Exec("INSERT INTO shop_shipments(id,organization_id,order_id,tracking_number,carrier,status) VALUES(?,?,?,?,?,'shipped')", shipmentID, organizationID, in.OrderID, in.TrackingNumber, in.Carrier); err != nil {
		return shopShipmentResult{}, err
	}
	result := shopShipmentResult{OrderID: in.OrderID, TrackingNumber: in.TrackingNumber, Carrier: in.Carrier, Status: "shipped", Simulated: true}
	responseBody, err := json.Marshal(result)
	if err != nil {
		return shopShipmentResult{}, err
	}
	if _, err = tx.Exec("INSERT INTO idempotency_keys(id,organization_id,key,endpoint,request_hash,response_status,response_body) VALUES(?,?,?,'shop_shipment',?,200,?)", token(), organizationID, in.IdempotencyKey, fingerprint, string(responseBody)); err != nil {
		return shopShipmentResult{}, err
	}
	before, _ := json.Marshal(map[string]string{"paymentStatus": paymentStatus, "fulfillmentStatus": fulfillmentStatus})
	after, _ := json.Marshal(result)
	if _, err = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,before_value,after_value) VALUES(?,?,?,?,?,'shop_order',?,?,?)", token(), organizationID, actorID, actorRole, "shop.shipment.create", in.OrderID, string(before), string(after)); err != nil {
		return shopShipmentResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return shopShipmentResult{}, err
	}
	return result, nil
}

func (a *App) shopShipmentAPI(w http.ResponseWriter, r *http.Request) {
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
	if grant == nil || grant.Scope != "back" {
		fail(w, 403, "Shop back-office access is required")
		return
	}
	if grant.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	var in shopShipmentInput
	if body(r, &in) != nil {
		fail(w, 400, errShipmentInvalid.Error())
		return
	}
	result, err := createShopShipment(a.db, id.OrganizationID, id.UserID, grant.Role, in)
	switch {
	case err == nil:
		ok(w, result)
	case errors.Is(err, errShipmentInvalid):
		fail(w, 400, err.Error())
	case errors.Is(err, errShipmentNotFound):
		fail(w, 404, err.Error())
	case errors.Is(err, errShipmentConflict):
		fail(w, 409, err.Error())
	default:
		fail(w, 503, "Shipment transaction failed")
	}
}
