package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type Dataset struct {
	Key, Table, Label string
	Front             []string
}

var datasets = map[string][]Dataset{
	"shop": {{"products", "shop_products", "Products", []string{"id", "name", "description", "category_id"}}, {"categories", "shop_categories", "Categories", []string{"id", "name"}}, {"variants", "shop_variants", "Product variants", []string{"id", "product_id", "title", "price"}}, {"orders", "shop_orders", "Orders", []string{"id", "order_number", "fulfillment_status", "payment_status", "tracking_number", "total_amount", "created_at"}}, {"items", "shop_order_items", "Order items", []string{"id", "order_id", "product_name", "variant_title", "quantity", "line_total", "created_at"}}, {"returns", "shop_returns", "Returns", nil}, {"reviews", "shop_reviews", "Reviews", nil}},
}

func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}
func queryMaps(db *sql.DB, query string, args ...any) ([]map[string]any, error) {
	rows, e := db.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	cols, e := rows.Columns()
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for rows.Next() {
		v := make([]any, len(cols))
		p := make([]any, len(cols))
		for i := range v {
			p[i] = &v[i]
		}
		if e = rows.Scan(p...); e != nil {
			return nil, e
		}
		m := map[string]any{}
		for i, c := range cols {
			if b, yes := v[i].([]byte); yes {
				v[i] = string(b)
			}
			m[camel(c)] = v[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (a *App) stationAPI(w http.ResponseWriter, r *http.Request) {
	site, action := r.PathValue("site"), r.PathValue("action")
	if !knownSite(site) {
		fail(w, 404, "Workspace not found")
		return
	}
	id, e := a.identity(r)
	if e != nil {
		fail(w, 401, e.Error())
		return
	}
	g := id.grant(site)
	if g == nil {
		fail(w, 403, "No access to this workspace")
		return
	}
	if action == "data" && r.Method == "GET" {
		front := r.URL.Query().Get("view") == "front"
		if !front && g.Scope != "back" {
			fail(w, 403, "Front-office accounts cannot read back-office data")
			return
		}
		out := map[string]any{}
		for _, d := range datasets[site] {
			if front && len(d.Front) == 0 {
				continue
			}
			columns := "*"
			if front {
				columns = strings.Join(d.Front, ",")
				if site == "shop" && d.Key == "variants" {
					columns += ",MAX(stock_quantity-reserved_quantity,0) AS available_quantity"
				}
			}
			query := "SELECT " + columns + " FROM " + d.Table + " WHERE organization_id=?"
			args := []any{id.OrganizationID}
			if front && site == "shop" && d.Key == "products" {
				query += " AND is_active=1"
			}
			if front && site == "shop" && d.Key == "variants" {
				query += " AND is_active=1 AND product_id IN (SELECT id FROM shop_products WHERE organization_id=? AND is_active=1)"
				args = append(args, id.OrganizationID)
			}
			if front && site == "shop" && d.Key == "orders" {
				query += " AND user_id=?"
				args = append(args, id.UserID)
			}
			if front && site == "shop" && d.Key == "items" {
				query += " AND order_id IN (SELECT id FROM shop_orders WHERE organization_id=? AND user_id=?)"
				args = append(args, id.OrganizationID, id.UserID)
			}
			rows, err := queryMaps(a.db, query+" ORDER BY rowid DESC LIMIT 500", args...)
			if err != nil {
				fail(w, 500, "Unable to read workspace data: "+d.Key)
				return
			}
			out[d.Key] = rows
		}
		if front && site == "shop" {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		ok(w, out)
		return
	}
	if action == "users" {
		if g.Role != "admin" || g.Scope != "back" {
			fail(w, 403, "Only workspace administrators can manage accounts")
			return
		}
		if r.Method == "GET" {
			rows, e := queryMaps(a.db, `SELECT a.user_id,a.email,u.display_name,g.role,g.scope,g.enabled FROM station_accounts a JOIN users u ON u.id=a.user_id JOIN station_grants g ON g.user_id=a.user_id WHERE a.organization_id=? AND g.site=?`, id.OrganizationID, site)
			if e != nil {
				fail(w, 500, "Unable to read accounts")
				return
			}
			ok(w, rows)
			return
		}
		if r.Method == "POST" {
			a.manageUser(w, r, id, site)
			return
		}
	}
	if action == "print" && r.Method == "GET" {
		if g.Scope != "back" {
			fail(w, 403, "Print center requires back-office access")
			return
		}
		a.printData(w, r, id, site)
		return
	}
	fail(w, 501, "This operation has not been migrated to Go. No changes were made")
}
func (a *App) manageUser(w http.ResponseWriter, r *http.Request, id *Identity, site string) {
	var input struct {
		UserID   string `json:"userId"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Scope    string `json:"scope"`
		Enabled  bool   `json:"enabled"`
	}
	if body(r, &input) != nil || (input.Role != "admin" && input.Role != "operator" && input.Role != "readonly") || (input.Scope != "front" && input.Scope != "back") || (input.Role == "admin" && input.Scope != "back") {
		fail(w, 400, "Invalid account or permission data")
		return
	}
	if input.UserID == id.UserID {
		fail(w, 409, "Ask another administrator to change your own permissions")
		return
	}
	hash := ""
	var e error
	if input.UserID == "" {
		input.Email = strings.ToLower(strings.TrimSpace(input.Email))
		if !strings.Contains(input.Email, "@") || len(input.Email) > 254 || len(input.Name) == 0 || len(input.Name) > 80 || len(input.Password) < 12 || len(input.Password) > 256 {
			fail(w, 400, "Provide a name, email and password of at least 12 characters")
			return
		}
		hash, e = passwordHash(input.Password, "")
		if e != nil {
			fail(w, 500, "Password processing failed")
			return
		}
	}
	tx, e := a.db.Begin()
	if e != nil {
		fail(w, 503, "Unable to begin account transaction")
		return
	}
	defer tx.Rollback()
	target := input.UserID
	if target == "" {
		target = token()
		if _, e = tx.Exec("INSERT INTO users(id,email,display_name) VALUES(?,?,?)", target, input.Email, input.Name); e == nil {
			_, e = tx.Exec("INSERT INTO station_accounts(user_id,organization_id,email,password_hash) VALUES(?,?,?,?)", target, id.OrganizationID, input.Email, hash)
		}
	} else {
		var n int
		e = tx.QueryRow("SELECT count(*) FROM station_accounts WHERE user_id=? AND organization_id=?", target, id.OrganizationID).Scan(&n)
		if e == nil && n != 1 {
			e = errors.New("Account does not belong to this organization")
		}
	}
	if e == nil {
		_, e = tx.Exec(`INSERT INTO station_grants(user_id,site,role,scope,enabled) VALUES(?,?,?,?,?) ON CONFLICT(user_id,site) DO UPDATE SET role=excluded.role,scope=excluded.scope,enabled=excluded.enabled`, target, site, input.Role, input.Scope, input.Enabled)
	}
	if e == nil {
		audit, _ := json.Marshal(map[string]any{"site": site, "role": input.Role, "scope": input.Scope, "enabled": input.Enabled})
		_, e = tx.Exec("INSERT INTO audit_events(id,organization_id,actor_id,actor_role,action,entity_type,entity_id,after_value) VALUES(?,?,?,'admin','STATION_GRANT','ACCOUNT',?,?)", token(), id.OrganizationID, id.UserID, target, string(audit))
	}
	if e == nil {
		e = tx.Commit()
	}
	if e != nil {
		fail(w, 409, "Account update failed. Check email uniqueness and organization membership")
		return
	}
	ok(w, map[string]string{"userId": target})
}

type PrintKind struct{ Site, Dataset, Title, Lines, ForeignKey string }

var printKinds = map[string]PrintKind{
	"shop-product-label": {"shop", "variants", "Product variant label", "", ""}, "shop-order": {"shop", "orders", "Commerce order", "shop_order_items", "order_id"},
}

func (a *App) printData(w http.ResponseWriter, r *http.Request, id *Identity, site string) {
	kind, exists := printKinds[r.URL.Query().Get("kind")]
	if !exists || kind.Site != site {
		fail(w, 400, "Unsupported print format")
		return
	}
	recordID := r.URL.Query().Get("id")
	table := ""
	for _, d := range datasets[site] {
		if d.Key == kind.Dataset {
			table = d.Table
		}
	}
	rows, e := queryMaps(a.db, "SELECT * FROM "+table+" WHERE organization_id=? AND id=?", id.OrganizationID, recordID)
	if e != nil || len(rows) != 1 {
		fail(w, 404, "Document not found in this organization")
		return
	}
	lines := []map[string]any{}
	if kind.Lines != "" {
		lines, e = queryMaps(a.db, fmt.Sprintf("SELECT * FROM %s WHERE organization_id=? AND %s=?", kind.Lines, kind.ForeignKey), id.OrganizationID, recordID)
		if e != nil {
			fail(w, 500, "Unable to read document details")
			return
		}
	}
	ok(w, map[string]any{"title": kind.Title, "record": rows[0], "lines": lines, "printedBy": id.Name, "organizationId": id.OrganizationID})
}
