package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func appForTest(t *testing.T) *App {
	t.Helper()
	db, e := openDB(filepath.Join(t.TempDir(), "suite.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	a := &App{db: db}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func TestShopPaymentSchemaSupportsFreshUpgradeAndReopen(t *testing.T) {
	fresh, err := openDB(filepath.Join(t.TempDir(), "fresh.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Close(); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := openDB(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec("ALTER TABLE shop_payment_attempts RENAME COLUMN actor_id TO old_actor_id"); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec("ALTER TABLE shop_payment_attempts RENAME COLUMN actor_role TO old_actor_role; ALTER TABLE shop_orders DROP COLUMN coupon_code; UPDATE shop_repository_schema SET version=1"); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := openDB(legacyPath)
	if err != nil {
		t.Fatalf("upgrade v1 schema: %v", err)
	}
	if err = upgraded.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openDB(legacyPath)
	if err != nil {
		t.Fatalf("reopen upgraded schema: %v", err)
	}
	defer reopened.Close()
	var version int
	if err = reopened.QueryRow("SELECT version FROM shop_repository_schema").Scan(&version); err != nil || version != 3 {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
}
func request(a *App, method, path string, input any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	b, _ := json.Marshal(input)
	r := httptest.NewRequest(method, "http://localhost"+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func login(t *testing.T, a *App, email, space string) []*http.Cookie {
	return loginSite(t, a, email, space, repositorySite)
}
func loginSite(t *testing.T, a *App, email, space, site string) []*http.Cookie {
	t.Helper()
	w := request(a, "POST", "/api/auth/login", map[string]string{"email": email, "password": testPassword, "site": site}, &http.Cookie{Name: demoCookie, Value: space})
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	return w.Result().Cookies()
}
func TestSchemaDatasetAndPrintContracts(t *testing.T) {
	a := appForTest(t)
	for site, sets := range datasets {
		if site != repositorySite {
			continue
		}
		for _, d := range sets {
			cols := "*"
			if len(d.Front) > 0 {
				cols = ""
				for i, c := range d.Front {
					if i > 0 {
						cols += ","
					}
					cols += c
				}
			}
			if _, e := queryMaps(a.db, "SELECT "+cols+" FROM "+d.Table+" WHERE organization_id=?", "none"); e != nil {
				t.Errorf("%s/%s: %v", site, d.Key, e)
			}
		}
	}
	for key, k := range printKinds {
		if !strings.HasPrefix(key, repositorySite+"-") {
			continue
		}
		if k.Lines != "" {
			if _, e := queryMaps(a.db, "SELECT * FROM "+k.Lines+" WHERE organization_id=? AND "+k.ForeignKey+"=?", "none", "none"); e != nil {
				t.Errorf("print %s: %v", key, e)
			}
		}
	}
}
func TestLoginScopeTamperingRevocationIsolation(t *testing.T) {
	a := appForTest(t)
	space := token()
	admin := login(t, a, "admin@suite.test", space)
	front := login(t, a, "front@suite.test", space)
	if w := request(a, "GET", "/api/stations/"+repositorySite+"/data", nil, front...); w.Code != 403 {
		t.Fatal("front accessed back office", w.Code)
	}
	if w := request(a, "GET", "/api/stations/"+repositorySite+"/data?view=front", nil, front...); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	tampered := append(front, &http.Cookie{Name: "demo_user_role", Value: "admin"})
	if w := request(a, "GET", "/api/stations/"+repositorySite+"/users", nil, tampered...); w.Code != 403 {
		t.Fatal("role cookie elevated permissions")
	}
	if w := request(a, "GET", "/api/stations/"+repositorySite+"/data", nil); w.Code != 401 {
		t.Fatal("anonymous accepted")
	}
	other := login(t, a, "admin@suite.test", token())
	w := request(a, "GET", "/api/auth/me", nil, other...)
	var doc struct {
		Data Identity `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &doc)
	otherID := doc.Data.UserID
	w = request(a, "POST", "/api/stations/"+repositorySite+"/users", map[string]any{"userId": otherID, "role": "readonly", "scope": "back", "enabled": true}, admin...)
	if w.Code != 409 {
		t.Fatal("cross organization grant accepted", w.Code)
	}
	w = request(a, "GET", "/api/auth/me", nil, front...)
	json.Unmarshal(w.Body.Bytes(), &doc)
	frontID := doc.Data.UserID
	w = request(a, "POST", "/api/stations/"+repositorySite+"/users", map[string]any{"userId": frontID, "role": "operator", "scope": "front", "enabled": false}, admin...)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = request(a, "GET", "/api/stations/"+repositorySite+"/data?view=front", nil, front...); w.Code != 403 {
		t.Fatal("revocation was not immediate")
	}
	request(a, "POST", "/api/auth/logout", map[string]any{}, admin...)
	if w = request(a, "GET", "/api/auth/me", nil, admin...); w.Code != 401 {
		t.Fatal("revoked session accepted")
	}
}
func TestOriginAndPassword(t *testing.T) {
	a := appForTest(t)
	r := httptest.NewRequest("POST", "http://localhost/api/auth/logout", nil)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF accepted")
	}
	h, e := passwordHash("long-password", "")
	if e != nil || !passwordOK("long-password", h) || passwordOK("bad", h) {
		t.Fatal("password verification failed")
	}
}

func TestAdditionalStationPrintsRequireAuthorizationAndStayInTenant(t *testing.T) {
	a := appForTest(t)
	space := token()
	otherSpace := token()
	for _, tc := range []struct{ site, dataset, kind string }{
		{"crm", "accounts", "crm-account-label"}, {"crm", "quotes", "crm-quote"},
		{"dining", "menuItems", "dining-menu-label"}, {"dining", "orders", "dining-order"},
		{"shop", "variants", "shop-product-label"}, {"shop", "orders", "shop-order"},
	} {
		if tc.site != repositorySite {
			continue
		}
		t.Run(tc.kind, func(t *testing.T) {
			admin := loginSite(t, a, "admin@suite.test", space, tc.site)
			front := loginSite(t, a, "front@suite.test", space, tc.site)
			other := loginSite(t, a, "admin@suite.test", otherSpace, tc.site)
			w := request(a, "GET", "/api/stations/"+tc.site+"/data?view=back", nil, admin...)
			var result struct {
				Data map[string][]map[string]any `json:"data"`
			}
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			rows := result.Data[tc.dataset]
			if len(rows) == 0 {
				t.Fatalf("missing fixture: %s", tc.dataset)
			}
			path := "/api/stations/" + tc.site + "/print?kind=" + tc.kind + "&id=" + rows[0]["id"].(string)
			if w = request(a, "GET", path, nil, admin...); w.Code != 200 {
				t.Fatalf("admin: %d %s", w.Code, w.Body.String())
			}
			if w = request(a, "GET", path, nil, front...); w.Code != 403 {
				t.Fatalf("front: %d", w.Code)
			}
			if w = request(a, "GET", path, nil, other...); w.Code != 404 {
				t.Fatalf("other tenant: %d", w.Code)
			}
		})
	}
}

func TestPreviewFramePolicy(t *testing.T) {
	a := appForTest(t)
	for _, tc := range []struct{ path, policy string }{
		{"/" + repositorySite + "/front?preview=mobile", "frame-ancestors 'self'"},
		{"/" + repositorySite + "/back?preview=workspace", "frame-ancestors 'self'"},
		{"/" + repositorySite + "/front", "frame-ancestors 'none'"},
		{"/" + repositorySite + "/users?preview=mobile", "frame-ancestors 'none'"},
		{"/api/auth/me?preview=mobile", "frame-ancestors 'none'"},
	} {
		w := request(a, "GET", tc.path, nil)
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), tc.policy) {
			t.Errorf("%s: %s", tc.path, w.Header().Get("Content-Security-Policy"))
		}
	}
}
