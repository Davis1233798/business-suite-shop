package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const checkoutLabTTL = 30 * time.Minute
const checkoutLabMaxPerOrganization = 10
const checkoutLabMaxTotal = 100

type checkoutLab struct {
	id, organizationID, variantID string
	db                            *sql.DB
	createdAt                     time.Time
	capacity                      int
	active                        int
}

func (a *App) cleanupCheckoutLabs(now time.Time) {
	a.labMu.Lock()
	defer a.labMu.Unlock()
	a.cleanupCheckoutLabsLocked(now)
}

func (a *App) cleanupCheckoutLabsLocked(now time.Time) {
	if a.checkoutLabs == nil {
		a.checkoutLabs = map[string]*checkoutLab{}
	}
	for id, lab := range a.checkoutLabs {
		if lab.active == 0 && now.Sub(lab.createdAt) >= checkoutLabTTL {
			_ = lab.db.Close()
			delete(a.checkoutLabs, id)
		}
	}
	for {
		oldestAll, countAll := (*checkoutLab)(nil), 0
		for _, lab := range a.checkoutLabs {
			if lab.active > 0 {
				continue
			}
			countAll++
			if oldestAll == nil || lab.createdAt.Before(oldestAll.createdAt) {
				oldestAll = lab
			}
		}
		if len(a.checkoutLabs) <= checkoutLabMaxTotal || oldestAll == nil || countAll == 0 {
			break
		}
		_ = oldestAll.db.Close()
		delete(a.checkoutLabs, oldestAll.id)
	}
}

func (a *App) checkoutLabForTestMaybe(id string) (*checkoutLab, bool) {
	a.labMu.Lock()
	defer a.labMu.Unlock()
	lab, ok := a.checkoutLabs[id]
	return lab, ok
}

func (a *App) checkoutLabForTest(id string) *checkoutLab {
	lab, _ := a.checkoutLabForTestMaybe(id)
	return lab
}

func (a *App) checkoutLabAPI(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("site") != "shop" {
		fail(w, 404, "Checkout lab is unavailable")
		return
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "Use POST")
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
	if g.Role == "readonly" {
		fail(w, 403, "A write-enabled role is required")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.cleanupCheckoutLabs(time.Now())
	var in struct {
		Action   string `json:"action"`
		RunID    string `json:"runId"`
		Key      string `json:"key"`
		Capacity int    `json:"capacity"`
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
		db, e := openDB(":memory:")
		if e != nil {
			fail(w, 503, "Could not create isolated checkout database")
			return
		}
		run, product, variant, category := token(), token(), token(), token()
		_, e = db.Exec("INSERT INTO shop_categories(id,organization_id,name,slug) VALUES(?,?,?,?)", category, id.OrganizationID, "Concurrency Lab", "lab-"+run)
		if e == nil {
			_, e = db.Exec("INSERT INTO shop_products(id,organization_id,name,slug,description,category_id,is_active) VALUES(?,?,?,?,?,?,1)", product, id.OrganizationID, "Isolated checkout product", "lab-"+run, "Temporary product for checkout concurrency rehearsal", category)
		}
		if e == nil {
			_, e = db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,?,?,0,1)", variant, id.OrganizationID, product, "LAB-"+run[:12], "Lab unit", 100, in.Capacity)
		}
		if e != nil {
			_ = db.Close()
			fail(w, 503, "Could not seed isolated checkout database")
			return
		}
		lab := &checkoutLab{id: run, organizationID: id.OrganizationID, variantID: variant, db: db, createdAt: time.Now(), capacity: in.Capacity}
		a.labMu.Lock()
		a.cleanupCheckoutLabsLocked(time.Now())
		orgCount := 0
		for _, old := range a.checkoutLabs {
			if old.organizationID == id.OrganizationID {
				orgCount++
			}
		}
		if orgCount >= checkoutLabMaxPerOrganization || len(a.checkoutLabs) >= checkoutLabMaxTotal {
			a.labMu.Unlock()
			_ = db.Close()
			fail(w, 429, "Checkout lab history limit reached; wait for expired runs to be cleaned")
			return
		}
		a.checkoutLabs[run] = lab
		a.labMu.Unlock()
		time.AfterFunc(checkoutLabTTL, func() { a.cleanupCheckoutLabs(time.Now()) })
		ok(w, map[string]any{"runId": run, "capacity": in.Capacity, "unitPrice": 100})
		return
	}
	if in.Action != "checkout" || in.RunID == "" || len(strings.TrimSpace(in.Key)) < 1 || len(in.Key) > 128 {
		fail(w, 400, "Provide a valid checkout action, run and request key")
		return
	}
	a.labMu.Lock()
	lab := a.checkoutLabs[in.RunID]
	if lab != nil && lab.organizationID == id.OrganizationID {
		lab.active++
	}
	a.labMu.Unlock()
	if lab == nil || lab.organizationID != id.OrganizationID {
		fail(w, 404, "Checkout run not found in this workspace")
		return
	}
	defer func() {
		a.labMu.Lock()
		lab.active--
		if lab.active == 0 && time.Since(lab.createdAt) >= checkoutLabTTL {
			_ = lab.db.Close()
			delete(a.checkoutLabs, lab.id)
		}
		a.labMu.Unlock()
	}()
	result, e := checkoutOrder(r.Context(), lab.db, id.OrganizationID, id.UserID, checkoutInput{VariantID: lab.variantID, Quantity: 1, Key: strings.TrimSpace(in.Key), Email: "lab@example.invalid", Address: map[string]any{"name": "Checkout lab", "address": "Isolated fixture"}})
	if e == errCheckoutSoldOut {
		ok(w, map[string]any{"outcome": "sold_out", "remaining": 0, "capacity": lab.capacity})
		return
	}
	if e == errCheckoutIdempotency {
		fail(w, 409, e.Error())
		return
	}
	if e != nil {
		fail(w, 503, "Checkout transaction failed")
		return
	}
	var stock int
	_ = lab.db.QueryRow("SELECT stock_quantity FROM shop_variants WHERE id=?", lab.variantID).Scan(&stock)
	result["remaining"] = stock
	result["capacity"] = lab.capacity
	result["outcome"] = "accepted"
	if result["duplicate"] == true {
		result["outcome"] = "duplicate"
	}
	ok(w, result)
}
