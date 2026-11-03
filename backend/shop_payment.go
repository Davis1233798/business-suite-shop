package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var paymentReferencePattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
var errPaymentInvalid = errors.New("invalid payment callback")
var errPaymentNotFound = errors.New("order not found")
var errPaymentConflict = errors.New("payment callback conflicts with the order or prior attempt")

type paymentCallbackInput struct {
	OrderID          string `json:"orderId"`
	AttemptReference string `json:"attemptReference"`
	Amount           int    `json:"amount"`
	Status           string `json:"status"`
}

type paymentCallbackResult struct {
	OrderID       string `json:"orderId"`
	PaymentStatus string `json:"paymentStatus"`
	AttemptStatus string `json:"attemptStatus"`
	Duplicate     bool   `json:"duplicate"`
}

func simulateShopPayment(db *sql.DB, organizationID, actorID, actorRole string, in paymentCallbackInput, now time.Time) (paymentCallbackResult, error) {
	in.OrderID = strings.TrimSpace(in.OrderID)
	in.AttemptReference = strings.TrimSpace(in.AttemptReference)
	if in.OrderID == "" || len(in.OrderID) > 128 || !paymentReferencePattern.MatchString(in.AttemptReference) || in.Amount <= 0 || (in.Status != "success" && in.Status != "failed") {
		return paymentCallbackResult{}, errPaymentInvalid
	}
	tx, err := db.Begin()
	if err != nil {
		return paymentCallbackResult{}, err
	}
	defer tx.Rollback()
	var orderOwner, paymentStatus string
	var amount int
	err = tx.QueryRow("SELECT COALESCE(user_id, ''),total_amount,payment_status FROM shop_orders WHERE id=? AND organization_id=?", in.OrderID, organizationID).Scan(&orderOwner, &amount, &paymentStatus)
	if err == sql.ErrNoRows {
		return paymentCallbackResult{}, errPaymentNotFound
	}
	if err != nil {
		return paymentCallbackResult{}, err
	}
	if actorRole != "admin" && orderOwner != actorID {
		return paymentCallbackResult{}, errPaymentNotFound
	}
	if in.Amount != amount {
		return paymentCallbackResult{}, errPaymentConflict
	}
	var oldOrder, oldActor, oldRole, oldStatus string
	var oldAmount int
	err = tx.QueryRow(`SELECT order_id,actor_id,actor_role,amount,status FROM shop_payment_attempts WHERE organization_id=? AND attempt_reference=?`, organizationID, in.AttemptReference).Scan(&oldOrder, &oldActor, &oldRole, &oldAmount, &oldStatus)
	if err == nil {
		if oldOrder != in.OrderID || oldActor != actorID || oldRole != actorRole || oldAmount != in.Amount || oldStatus != in.Status {
			return paymentCallbackResult{}, errPaymentConflict
		}
		result := paymentCallbackResult{OrderID: in.OrderID, PaymentStatus: paymentStatus, AttemptStatus: oldStatus, Duplicate: true}
		if err = tx.Commit(); err != nil {
			return paymentCallbackResult{}, err
		}
		return result, nil
	}
	if err != sql.ErrNoRows {
		return paymentCallbackResult{}, err
	}
	var reservationID, expiresAt, reservationStatus string
	err = tx.QueryRow(`SELECT id,expires_at,status FROM shop_inventory_reservations WHERE organization_id=? AND cart_id=? AND session_id LIKE 'shop-go-checkout:%' ORDER BY created_at DESC,id DESC LIMIT 1`, organizationID, in.OrderID).Scan(&reservationID, &expiresAt, &reservationStatus)
	if err == sql.ErrNoRows {
		return paymentCallbackResult{}, errPaymentConflict
	}
	if err != nil {
		return paymentCallbackResult{}, err
	}
	if paymentStatus == "pending" || paymentStatus == "failed" {
		if reservationStatus != "active" {
			return paymentCallbackResult{}, errPaymentConflict
		}
		parsedExpiry, parseErr := time.Parse(time.RFC3339Nano, expiresAt)
		if parseErr != nil {
			return paymentCallbackResult{}, errPaymentConflict
		}
		if !parsedExpiry.After(now) {
			if err = expireShopOrderTx(tx, organizationID, in.OrderID, reservationID); err != nil {
				return paymentCallbackResult{}, err
			}
			if err = tx.Commit(); err != nil {
				return paymentCallbackResult{}, err
			}
			return paymentCallbackResult{}, errPaymentConflict
		}
	} else if paymentStatus != "paid" {
		return paymentCallbackResult{}, errPaymentConflict
	}
	payloadBytes, _ := json.Marshal(in)
	if _, err = tx.Exec(`INSERT INTO shop_payment_attempts(id,organization_id,order_id,attempt_reference,amount,provider,status,callback_payload,actor_id,actor_role) VALUES(?,?,?,?,?,?,?,?,?,?)`, token(), organizationID, in.OrderID, in.AttemptReference, in.Amount, "simulated", in.Status, string(payloadBytes), actorID, actorRole); err != nil {
		return paymentCallbackResult{}, err
	}
	if in.Status == "success" {
		res, e := tx.Exec(`UPDATE shop_orders SET payment_status='paid' WHERE id=? AND organization_id=? AND payment_status IN ('pending','failed')`, in.OrderID, organizationID)
		if e != nil {
			return paymentCallbackResult{}, e
		}
		changed, e := res.RowsAffected()
		if e != nil {
			return paymentCallbackResult{}, e
		}
		if changed == 1 {
			res, e = tx.Exec(`UPDATE shop_inventory_reservations SET status='consumed' WHERE id=? AND organization_id=? AND status='active'`, reservationID, organizationID)
			if e != nil {
				return paymentCallbackResult{}, e
			}
			changed, e = res.RowsAffected()
			if e != nil || changed != 1 {
				if e != nil {
					return paymentCallbackResult{}, e
				}
				return paymentCallbackResult{}, errPaymentConflict
			}
			payload, _ := json.Marshal(map[string]string{"orderId": in.OrderID, "attemptReference": in.AttemptReference})
			if _, e = tx.Exec(`INSERT INTO outbox_events(id,organization_id,event_type,payload) VALUES(?,?,'shop.paid',?)`, token(), organizationID, string(payload)); e != nil {
				return paymentCallbackResult{}, e
			}
			paymentStatus = "paid"
		}
	} else if paymentStatus != "paid" {
		res, e := tx.Exec(`UPDATE shop_orders SET payment_status='failed' WHERE id=? AND organization_id=? AND payment_status='pending'`, in.OrderID, organizationID)
		if e != nil {
			return paymentCallbackResult{}, e
		}
		changed, e := res.RowsAffected()
		if e != nil {
			return paymentCallbackResult{}, e
		}
		if changed == 1 {
			paymentStatus = "failed"
		}
	}
	if err = tx.Commit(); err != nil {
		return paymentCallbackResult{}, err
	}
	return paymentCallbackResult{OrderID: in.OrderID, PaymentStatus: paymentStatus, AttemptStatus: in.Status}, nil
}

func expireShopOrderTx(tx *sql.Tx, organizationID, orderID, reservationID string) error {
	var variant string
	var quantity int
	if err := tx.QueryRow(`SELECT variant_id,quantity FROM shop_inventory_reservations WHERE id=? AND organization_id=? AND status='active'`, reservationID, organizationID).Scan(&variant, &quantity); err != nil {
		return err
	}
	res, err := tx.Exec(`UPDATE shop_inventory_reservations SET status='expired' WHERE id=? AND organization_id=? AND status='active'`, reservationID, organizationID)
	if err != nil {
		return err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errPaymentConflict
	}
	if _, err = tx.Exec(`UPDATE shop_variants SET stock_quantity=stock_quantity+? WHERE id=? AND organization_id=?`, quantity, variant, organizationID); err != nil {
		return err
	}
	res, err = tx.Exec(`UPDATE shop_orders SET payment_status='expired' WHERE id=? AND organization_id=? AND payment_status IN ('pending','failed')`, orderID, organizationID)
	if err != nil {
		return err
	}
	changed, err = res.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errPaymentConflict
	}
	return nil
}

func (a *App) shopPaymentCallbackAPI(w http.ResponseWriter, r *http.Request) {
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
	g := id.grant("shop")
	if g == nil || g.Scope != "back" {
		fail(w, 403, "Shop back-office access is required")
		return
	}
	if g.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	var in paymentCallbackInput
	if body(r, &in) != nil {
		fail(w, 400, errPaymentInvalid.Error())
		return
	}
	result, err := simulateShopPayment(a.db, id.OrganizationID, id.UserID, g.Role, in, time.Now().UTC())
	switch err {
	case nil:
		ok(w, result)
	case errPaymentInvalid:
		fail(w, 400, err.Error())
	case errPaymentNotFound:
		fail(w, 404, err.Error())
	case errPaymentConflict:
		fail(w, 409, err.Error())
	default:
		fail(w, 503, "Payment transaction failed")
	}
}
