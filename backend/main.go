package main

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	_ "modernc.org/sqlite"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed migrations/*.sql migrations/fixtures.json migrations/shop_fixtures.json
var migrations embed.FS
var sites = []string{"mes", "erp", "pos", "crm", "dining", "shop"}

type App struct {
	db           *sql.DB
	assets       string
	labMu        sync.Mutex
	checkoutLabs map[string]*checkoutLab
}

func (a *App) Close() error {
	a.labMu.Lock()
	for id, lab := range a.checkoutLabs {
		_ = lab.db.Close()
		delete(a.checkoutLabs, id)
	}
	a.labMu.Unlock()
	return a.db.Close()
}

type Grant struct {
	Site    string `json:"site"`
	Role    string `json:"role"`
	Scope   string `json:"scope"`
	Enabled bool   `json:"enabled"`
}
type Identity struct {
	UserID         string  `json:"userId"`
	OrganizationID string  `json:"organizationId"`
	Email          string  `json:"email"`
	Name           string  `json:"name"`
	Grants         []Grant `json:"grants"`
}

const cookieName = "suite_session"
const demoCookie = "suite_demo_space"
const testPassword = "SuiteDemo!2026"

var demoPeople = []struct{ Email, Name, Role, Scope string }{{"admin@suite.test", "Administrator", "admin", "back"}, {"office@suite.test", "Back-office operator", "operator", "back"}, {"front@suite.test", "Front-office operator", "operator", "front"}, {"viewer@suite.test", "Read-only viewer", "readonly", "back"}}

func token() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func digest(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }
func passwordHash(password, salt string) (string, error) {
	if salt == "" {
		salt = token()
	}
	v, e := pbkdf2.Key(sha256.New, password, []byte(salt), 600000, 32)
	return "pbkdf2-sha256$600000$" + salt + "$" + hex.EncodeToString(v), e
}
func passwordOK(password, encoded string) bool {
	p := strings.Split(encoded, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" || p[1] != "600000" {
		return false
	}
	v, e := passwordHash(password, p[2])
	return e == nil && subtle.ConstantTimeCompare([]byte(v), []byte(encoded)) == 1
}

const repositorySite = "shop"

func knownSite(site string) bool {
	if site != repositorySite {
		return false
	}
	for _, s := range sites {
		if s == site {
			return true
		}
	}
	return false
}
func (i *Identity) grant(site string) *Grant {
	for j := range i.Grants {
		if i.Grants[j].Site == site && i.Grants[j].Enabled {
			return &i.Grants[j]
		}
	}
	return nil
}
func openDB(file string) (*sql.DB, error) {
	db, e := sql.Open("sqlite", file)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	if _, e = db.Exec("PRAGMA foreign_keys=ON; PRAGMA busy_timeout=10000; PRAGMA journal_mode=WAL;"); e != nil {
		return nil, e
	}
	tx, e := db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var n int
	if e = tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='organizations'").Scan(&n); e != nil {
		return nil, e
	}
	files := []string{}
	shopSchemaVersion := 0
	freshShopDatabase := n == 0
	if n == 0 {
		files = append(files, "shop_initial.sql")
	} else {
		var marker int
		if e = tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='shop_repository_schema'").Scan(&marker); e != nil {
			return nil, e
		}
		if marker == 0 {
			tx.Rollback()
			db.Close()
			return nil, fmt.Errorf("refusing existing database without shop repository marker; use a new database path")
		}
		if e = tx.QueryRow("SELECT version FROM shop_repository_schema LIMIT 1").Scan(&shopSchemaVersion); e != nil {
			return nil, e
		}
	}
	files = append(files, "shop_guards.sql")
	if !freshShopDatabase && shopSchemaVersion < 2 {
		files = append(files, "shop_payment_migration.sql")
	}
	if !freshShopDatabase && shopSchemaVersion < 3 {
		files = append(files, "shop_coupon_migration.sql")
	}
	files = append(files, "0004_station_identity.sql", "0005_traffic_lab.sql")
	for _, f := range files {
		b, err := migrations.ReadFile("migrations/" + f)
		if err != nil {
			return nil, err
		}
		if _, err = tx.Exec(string(b)); err != nil {
			return nil, fmt.Errorf("migration %s: %w", f, err)
		}
	}
	if _, e = tx.Exec("CREATE TABLE IF NOT EXISTS shop_repository_schema (version INTEGER NOT NULL); INSERT INTO shop_repository_schema(version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM shop_repository_schema); UPDATE shop_repository_schema SET version=3 WHERE version<3"); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return db, nil
}
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}
func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"success": false, "error": map[string]string{"message": msg}})
}
func ok(w http.ResponseWriter, data any) {
	writeJSON(w, 200, map[string]any{"success": true, "data": data})
}
func body(r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return errors.New("A JSON request is required")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 65537))
	dec.DisallowUnknownFields()
	if e := dec.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := dec.Decode(&extra); e != io.EOF {
		return errors.New("Invalid JSON")
	}
	return nil
}
func cookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode})
}
func sessionCookieName(r *http.Request) string {
	site := r.PathValue("site")
	if site == "" {
		site = r.URL.Query().Get("site")
	}
	if knownSite(site) {
		return cookieName + "_" + site
	}
	// Legacy callers without a site may use a single unambiguous station session.
	found := ""
	for _, candidate := range sites {
		name := cookieName + "_" + candidate
		if _, err := r.Cookie(name); err == nil {
			if found != "" {
				return ""
			}
			found = name
		}
	}
	return found
}
func (a *App) identity(r *http.Request) (*Identity, error) {
	c, e := r.Cookie(sessionCookieName(r))
	if e != nil || len(c.Value) != 64 {
		return nil, errors.New("Please sign in")
	}
	var id Identity
	e = a.db.QueryRow(`SELECT u.id,a.organization_id,a.email,u.display_name FROM station_sessions s JOIN station_accounts a ON a.user_id=s.user_id JOIN users u ON u.id=a.user_id WHERE s.token_hash=? AND s.expires_at>? AND a.disabled=0`, digest(c.Value), time.Now().UnixMilli()).Scan(&id.UserID, &id.OrganizationID, &id.Email, &id.Name)
	if e != nil {
		return nil, errors.New("Your session has expired")
	}
	rows, e := a.db.Query("SELECT site,role,scope,enabled FROM station_grants WHERE user_id=?", id.UserID)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	id.Grants = []Grant{}
	for rows.Next() {
		var g Grant
		if e = rows.Scan(&g.Site, &g.Role, &g.Scope, &g.Enabled); e != nil {
			return nil, e
		}
		id.Grants = append(id.Grants, g)
	}
	return &id, rows.Err()
}
func (a *App) session(w http.ResponseWriter, r *http.Request, user, site string) error {
	value := token()
	_, e := a.db.Exec("INSERT INTO station_sessions(token_hash,user_id,expires_at) VALUES(?,?,?)", digest(value), user, time.Now().Add(8*time.Hour).UnixMilli())
	if e == nil {
		cookie(w, r, cookieName+"_"+site, value, 8*3600)
	}
	return e
}
func (a *App) initializeDemo(space string) error {
	if len(space) != 64 {
		return errors.New("Invalid demo workspace")
	}
	org := "org_test_" + space
	var n int
	if e := a.db.QueryRow("SELECT count(*) FROM station_accounts WHERE organization_id=?", org).Scan(&n); e != nil {
		return e
	}
	if n == 4 {
		return nil
	}
	hash, e := passwordHash(testPassword, "")
	if e != nil {
		return e
	}
	tx, e := a.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT INTO organizations(id,name,slug) VALUES(?,?,?) ON CONFLICT DO NOTHING", org, "Isolated demo organization", org); e != nil {
		return e
	}
	for _, p := range demoPeople {
		id := org + "_" + strings.Split(p.Email, "@")[0]
		if _, e = tx.Exec("INSERT INTO users(id,email,display_name) VALUES(?,?,?) ON CONFLICT DO NOTHING", id, p.Email, p.Name); e != nil {
			return e
		}
		if _, e = tx.Exec("INSERT INTO station_accounts(user_id,organization_id,email,password_hash) VALUES(?,?,?,?) ON CONFLICT DO NOTHING", id, org, p.Email, hash); e != nil {
			return e
		}
		for _, s := range []string{repositorySite} {
			if _, e = tx.Exec("INSERT INTO station_grants(user_id,site,role,scope) VALUES(?,?,?,?) ON CONFLICT DO NOTHING", id, s, p.Role, p.Scope); e != nil {
				return e
			}
		}
	}
	if e = seedFixtures(tx, org); e != nil {
		return e
	}
	return tx.Commit()
}
func (a *App) auth(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action == "me" && r.Method == "GET" {
		id, e := a.identity(r)
		if e != nil {
			fail(w, 401, e.Error())
			return
		}
		ok(w, id)
		return
	}
	if action == "test-accounts" && r.Method == "GET" {
		people := []map[string]string{}
		for _, p := range demoPeople {
			people = append(people, map[string]string{"email": p.Email, "name": p.Name, "role": p.Role, "scope": p.Scope, "password": testPassword})
		}
		ok(w, people)
		return
	}
	if r.Method != "POST" {
		fail(w, 405, "Method not allowed")
		return
	}
	if action == "logout" {
		name := sessionCookieName(r)
		if name == "" {
			fail(w, 400, "Specify the workspace to sign out of")
			return
		}
		if c, e := r.Cookie(name); e == nil {
			a.db.Exec("DELETE FROM station_sessions WHERE token_hash=?", digest(c.Value))
		}
		cookie(w, r, name, "", -1)
		ok(w, map[string]bool{"signedOut": true})
		return
	}
	if action != "login" {
		fail(w, 404, "Operation not found")
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Site     string `json:"site"`
	}
	if body(r, &input) != nil || !knownSite(input.Site) || len(input.Email) > 254 || len(input.Password) > 256 {
		fail(w, 400, "Invalid sign-in data")
		return
	}
	space := ""
	if c, e := r.Cookie(demoCookie); e == nil && len(c.Value) == 64 {
		if _, e = hex.DecodeString(c.Value); e == nil {
			space = c.Value
		}
	}
	if space == "" {
		space = token()
	}
	org := "org_test_" + space
	// Shared database rate limits cannot be reset with a fresh process or cookie.
	limitKey := digest(strings.Split(r.RemoteAddr, ":")[0] + "|" + strings.ToLower(input.Email))
	now := time.Now().UnixMilli()
	_, e := a.db.Exec(`INSERT INTO station_login_limits(key,attempts,reset_at) VALUES(?,1,?) ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN station_login_limits.reset_at<? THEN 1 ELSE station_login_limits.attempts+1 END,reset_at=CASE WHEN station_login_limits.reset_at<? THEN ? ELSE station_login_limits.reset_at END`, limitKey, now+900000, now, now, now+900000)
	if e != nil {
		log.Printf("station sign-in rate-limit update failed: %v", e)
		fail(w, 503, "Sign-in service is temporarily unavailable")
		return
	}
	var attempts int
	if a.db.QueryRow("SELECT attempts FROM station_login_limits WHERE key=?", limitKey).Scan(&attempts) != nil || attempts > 20 {
		fail(w, 429, "Too many sign-in attempts. Try again later")
		return
	}
	if e = a.initializeDemo(space); e != nil {
		log.Print(e)
		fail(w, 500, "Unable to create demo workspace")
		return
	}
	cookie(w, r, demoCookie, space, 604800)
	var user, hash string
	e = a.db.QueryRow(`SELECT a.user_id,a.password_hash FROM station_accounts a JOIN station_grants g ON g.user_id=a.user_id WHERE a.organization_id=? AND a.email=? AND a.disabled=0 AND g.site=? AND g.enabled=1`, org, strings.ToLower(strings.TrimSpace(input.Email)), input.Site).Scan(&user, &hash)
	if e != nil || !passwordOK(input.Password, hash) {
		fail(w, 401, "Invalid credentials or workspace access")
		return
	}
	if e = a.session(w, r, user, input.Site); e != nil {
		fail(w, 500, "Unable to create session")
		return
	}
	ok(w, map[string]bool{"signedIn": true})
}
func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ancestors := "'none'"
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Block other workspaces before routing, including the dedicated traffic handler.
	for _, candidate := range sites {
		if candidate != repositorySite && ((len(parts) > 0 && parts[0] == candidate) || (len(parts) > 2 && parts[0] == "api" && parts[1] == "stations" && parts[2] == candidate)) {
			fail(w, http.StatusNotFound, "Workspace not found")
			return
		}
	}
	preview := r.URL.Query().Get("preview")
	if r.Method == http.MethodGet && len(parts) == 2 && knownSite(parts[0]) && (parts[1] == "front" || parts[1] == "back") && (preview == "mobile" || preview == "workspace") {
		ancestors = "'self'"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors "+ancestors+"; form-action 'self'; base-uri 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	if r.Method != "GET" && r.Method != "HEAD" {
		origin := r.Header.Get("Origin")
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		if origin != "" && origin != scheme+"://"+r.Host {
			fail(w, 403, "Cross-origin operations are not allowed")
			return
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		if a.db.Ping() != nil {
			fail(w, 503, "Database unavailable")
			return
		}
		ok(w, map[string]string{"backend": "go", "status": "ready", "revision": os.Getenv("SUITE_RELEASE_SHA")})
	})
	mux.HandleFunc("/api/auth/{action}", a.auth)
	mux.HandleFunc("/api/stations/{site}/traffic", a.trafficAPI)
	mux.HandleFunc("/api/stations/{site}/checkout", a.shopCheckoutAPI)
	mux.HandleFunc("/api/stations/{site}/payment/callback", a.shopPaymentCallbackAPI)
	mux.HandleFunc("/api/stations/{site}/ship", a.shopShipmentAPI)
	mux.HandleFunc("/api/stations/{site}/refund", a.shopRefundAPI)
	mux.HandleFunc("/api/stations/{site}/checkout-lab", a.checkoutLabAPI)
	mux.HandleFunc("/api/stations/{site}/{action}", a.stationAPI)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, 501, "This legacy business API has not been migrated to Go")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			fail(w, 405, "Method not allowed")
			return
		}
		clean := filepath.Clean(r.URL.Path)
		file := filepath.Join(a.assets, clean)
		if info, e := os.Stat(file); e == nil && !info.IsDir() {
			http.ServeFile(w, r, file)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(a.assets, "index.html"))
	})
	mux.ServeHTTP(w, r)
}
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		client := &http.Client{Timeout: 3 * time.Second}
		response, err := client.Get("http://127.0.0.1:8095/api/health")
		if err != nil {
			os.Exit(1)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	file := os.Getenv("SUITE_DB")
	if file == "" {
		file = ".data/shop-repository.sqlite"
	}
	if strings.TrimSpace(os.Getenv("SUITE_DATABASE_URL")) == "" {
		if e := os.MkdirAll(filepath.Dir(file), 0700); e != nil {
			log.Fatal(e)
		}
	}
	db, e := openConfiguredDB(file)
	if e != nil {
		log.Fatal(e)
	}
	assets := os.Getenv("SUITE_WEB")
	if assets == "" {
		assets = "../web-dist"
	}
	app := &App{db: db, assets: assets}
	defer app.Close()
	addr := os.Getenv("SUITE_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8095"
	}
	server := &http.Server{Addr: addr, Handler: app, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	listener, e := net.Listen("tcp", addr)
	if e != nil {
		log.Fatal(e)
	}
	log.Printf("Go business-suite ready at %s", listener.Addr().String())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
