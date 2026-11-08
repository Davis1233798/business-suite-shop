package main

import "net/http"

// operationsSnapshot reads operational records, never the isolated traffic lab.
func (a *App) operationsSnapshot(w http.ResponseWriter, r *http.Request, id *Identity, g *Grant, site string) {
	if r.Method != http.MethodGet {
		fail(w, 405, "Use GET")
		return
	}
	if g.Scope != "back" {
		fail(w, 403, "Back-office access required")
		return
	}
	if site != "shop" {
		fail(w, 404, "Workspace not found")
		return
	}
	var pending, unfulfilled, queued, retrying, paid, refunds int64
	e := a.db.QueryRow(`SELECT (SELECT COUNT(*) FROM shop_orders WHERE organization_id=? AND payment_status='pending'),(SELECT COUNT(*) FROM shop_orders WHERE organization_id=? AND payment_status='paid' AND fulfillment_status='unfulfilled'),(SELECT COUNT(*) FROM outbox_events WHERE organization_id=? AND event_type='shop.paid' AND status='pending'),(SELECT COUNT(*) FROM outbox_events WHERE organization_id=? AND event_type='shop.paid' AND status='pending' AND retry_count>0),(SELECT COALESCE(SUM(total_amount),0) FROM shop_orders WHERE organization_id=? AND payment_status='paid'),(SELECT COALESCE(SUM(r.refund_amount),0) FROM shop_returns r JOIN shop_orders o ON o.organization_id=r.organization_id AND o.id=r.order_id AND o.payment_status='paid' JOIN shop_order_items i ON i.organization_id=r.organization_id AND i.id=r.order_item_id AND i.order_id=o.id WHERE r.organization_id=? AND r.status='refunded')`, id.OrganizationID, id.OrganizationID, id.OrganizationID, id.OrganizationID, id.OrganizationID, id.OrganizationID).Scan(&pending, &unfulfilled, &queued, &retrying, &paid, &refunds)
	if e != nil {
		fail(w, 503, "Cannot read commerce snapshot")
		return
	}
	ok(w, map[string]any{"pendingPayment": pending, "paidUnfulfilled": unfulfilled, "pendingOutbox": queued, "retryingOutbox": retrying, "paidRevenue": paid, "refundedAmount": refunds, "netRevenue": paid - refunds})
}
