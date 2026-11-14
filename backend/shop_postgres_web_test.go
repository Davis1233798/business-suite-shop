package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestShopPostgresRuntimeSupportsStationWebFlows(t *testing.T) {
	dsn := os.Getenv("SHOP_POSTGRES_TEST_URL")
	if dsn == "" {
		t.Skip("set SHOP_POSTGRES_TEST_URL to a disposable PostgreSQL database")
	}
	t.Setenv("SUITE_DATABASE_URL", dsn)
	db, err := openConfiguredDB(filepath.Join(t.TempDir(), "must-not-be-used.sqlite"))
	if err != nil {
		t.Fatalf("open configured Shop database: %v", err)
	}
	a := &App{db: db}
	t.Cleanup(func() { _ = a.Close() })

	if w := request(a, "GET", "/api/health", nil); w.Code != http.StatusOK {
		t.Fatalf("health: %d %s", w.Code, w.Body.String())
	}
	space := token()
	admin := login(t, a, "admin@suite.test", space)
	front := loginSite(t, a, "front@suite.test", space, "shop")
	var identity struct {
		Data Identity `json:"data"`
	}
	if w := request(a, "GET", "/api/auth/me", nil, front...); w.Code != http.StatusOK {
		t.Fatalf("identity: %d %s", w.Code, w.Body.String())
	} else if err = json.Unmarshal(w.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	var org struct {
		Data Identity `json:"data"`
	}
	if w := request(a, "GET", "/api/auth/me", nil, admin...); w.Code != http.StatusOK {
		t.Fatalf("admin identity: %d %s", w.Code, w.Body.String())
	} else if err = json.Unmarshal(w.Body.Bytes(), &org); err != nil {
		t.Fatal(err)
	}
	if org.Data.OrganizationID != identity.Data.OrganizationID {
		t.Fatal("same workspace accounts resolved to different organizations")
	}

	var frontData struct {
		Data map[string][]map[string]any `json:"data"`
	}
	if w := request(a, "GET", "/api/stations/shop/data?view=front", nil, front...); w.Code != http.StatusOK {
		t.Fatalf("front catalog: %d %s", w.Code, w.Body.String())
	} else if err = json.Unmarshal(w.Body.Bytes(), &frontData); err != nil {
		t.Fatal(err)
	}
	if len(frontData.Data["products"]) == 0 || len(frontData.Data["variants"]) == 0 {
		t.Fatalf("front catalog did not load fixture rows: %#v", frontData.Data)
	}
	variantID := frontData.Data["variants"][0]["id"].(string)
	var backData struct {
		Data map[string][]map[string]any `json:"data"`
	}
	if w := request(a, "GET", "/api/stations/shop/data?view=back&dataset=products&search=%E6%A5%B5%E7%B0%A1&offset=0", nil, admin...); w.Code != http.StatusOK {
		t.Fatalf("back-office product search: %d %s", w.Code, w.Body.String())
	} else if err = json.Unmarshal(w.Body.Bytes(), &backData); err != nil {
		t.Fatal(err)
	}
	if len(backData.Data["products"]) == 0 {
		t.Fatalf("back-office search returned no fixture: %#v", backData.Data)
	}
	if w := request(a, "GET", "/api/stations/shop/users", nil, admin...); w.Code != http.StatusOK {
		t.Fatalf("accounts page: %d %s", w.Code, w.Body.String())
	}
	if w := request(a, "GET", "/api/stations/shop/operations-snapshot", nil, admin...); w.Code != http.StatusOK {
		t.Fatalf("operations snapshot: %d %s", w.Code, w.Body.String())
	}

	checkoutBody := map[string]any{
		"variantId": variantID, "quantity": 1, "idempotencyKey": "pg-web-checkout",
		"customerEmail":   identity.Data.Email,
		"shippingAddress": map[string]string{"name": "PG Buyer", "city": "Taipei", "address": "Test Road", "phone": "0900000000"},
	}
	w := request(a, "POST", "/api/stations/shop/checkout", checkoutBody, front...)
	if w.Code != http.StatusOK {
		t.Fatalf("checkout route: %d %s", w.Code, w.Body.String())
	}
	var checkout struct {
		Data map[string]any `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &checkout); err != nil {
		t.Fatal(err)
	}
	orderID := checkout.Data["orderId"].(string)
	itemID := ""
	productID := ""
	if err = db.QueryRow("SELECT id FROM shop_order_items WHERE organization_id=? AND order_id=? LIMIT 1", org.Data.OrganizationID, orderID).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow("SELECT product_id FROM shop_variants WHERE organization_id=? AND id=?", org.Data.OrganizationID, variantID).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	if w = request(a, "POST", "/api/stations/shop/payment/callback", map[string]any{"orderId": orderID, "attemptReference": "pg-web-payment", "amount": 160, "status": "success"}, admin...); w.Code != http.StatusOK {
		t.Fatalf("payment callback: %d %s", w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/stations/shop/ship", map[string]any{"orderId": orderID, "trackingNumber": "PG-TRACK-1", "carrier": "Pilot", "idempotencyKey": "pg-web-ship"}, admin...); w.Code != http.StatusOK {
		t.Fatalf("shipment: %d %s", w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/stations/shop/refund", map[string]any{"orderId": orderID, "orderItemId": itemID, "quantity": 1, "reason": "PostgreSQL web flow", "idempotencyKey": "pg-web-refund"}, admin...); w.Code != http.StatusOK {
		t.Fatalf("refund: %d %s", w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/stations/shop/after-sales", map[string]any{"action": "review", "orderId": orderID, "productId": productID, "rating": 5, "comment": "Works", "idempotencyKey": "pg-web-review"}, front...); w.Code != http.StatusOK {
		t.Fatalf("review route: %d %s", w.Code, w.Body.String())
	}
	if w = request(a, "GET", "/api/stations/shop/print?kind=shop-order&id="+orderID, nil, admin...); w.Code != http.StatusOK {
		t.Fatalf("print order: %d %s", w.Code, w.Body.String())
	}

	// Exercise the cart checkout's active coupon timestamp comparison.
	cartVariant := "pg-cart-" + token()
	if _, err = db.Exec("INSERT INTO shop_variants(id,organization_id,product_id,sku,title,price,stock_quantity,reserved_quantity,is_active) VALUES(?,?,?,?,?,100,5,0,1)", cartVariant, org.Data.OrganizationID, productID, "SKU-"+cartVariant, "Cart test unit"); err != nil {
		t.Fatal(err)
	}
	w = request(a, "POST", "/api/stations/shop/cart-checkout", map[string]any{
		"lines":      []map[string]any{{"variantId": cartVariant, "quantity": 1}},
		"couponCode": "SAVE10", "idempotencyKey": "pg-web-cart-checkout",
		"shippingAddress": map[string]string{"name": "PG Cart Buyer", "city": "Taipei", "address": "Cart Test Road", "phone": "0900000001"},
	}, front...)
	if w.Code != http.StatusOK {
		t.Fatalf("cart checkout and coupon: %d %s", w.Code, w.Body.String())
	}
	var cartCheckout struct {
		Data map[string]any `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &cartCheckout); err != nil {
		t.Fatal(err)
	}
	if cartCheckout.Data["totalAmount"] != float64(150) {
		t.Fatalf("cart coupon total: %#v", cartCheckout.Data)
	}

	// Exercise the separate commerce UI checkout/payment/shipping flow.
	business := func(action, key string) *httptest.ResponseRecorder {
		return request(a, "POST", "/api/stations/shop/business", map[string]any{"action": action, "variantId": variantID, "quantity": 1, "orderId": orderID, "tracking": "PG-BUSINESS-1", "idempotencyKey": key}, front...)
	}
	if w = business("checkout", "pg-business-checkout"); w.Code != http.StatusOK {
		t.Fatalf("business checkout: %d %s", w.Code, w.Body.String())
	}
	var businessCheckout struct {
		Data map[string]any `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &businessCheckout); err != nil {
		t.Fatal(err)
	}
	businessOrderID := businessCheckout.Data["orderId"].(string)
	if w = request(a, "POST", "/api/stations/shop/business", map[string]any{"action": "pay", "orderId": businessOrderID, "idempotencyKey": "pg-business-pay"}, front...); w.Code != http.StatusOK {
		t.Fatalf("business payment: %d %s", w.Code, w.Body.String())
	}
	if w = request(a, "POST", "/api/stations/shop/business", map[string]any{"action": "ship", "orderId": businessOrderID, "tracking": "PG-BUSINESS-2", "idempotencyKey": "pg-business-ship"}, admin...); w.Code != http.StatusOK {
		t.Fatalf("business shipping: %d %s", w.Code, w.Body.String())
	}

	traffic := request(a, "POST", "/api/stations/shop/traffic", map[string]any{"action": "prepare", "capacity": 2}, admin...)
	if traffic.Code != http.StatusOK {
		t.Fatalf("traffic prepare: %d %s", traffic.Code, traffic.Body.String())
	}
	var trafficData struct {
		Data map[string]any `json:"data"`
	}
	if err = json.Unmarshal(traffic.Body.Bytes(), &trafficData); err != nil {
		t.Fatal(err)
	}
	runID := fmt.Sprint(trafficData.Data["runId"])
	for _, key := range []string{"pg-a", "pg-b"} {
		w = request(a, "POST", "/api/stations/shop/traffic", map[string]any{"action": "submit", "runId": runID, "key": key}, admin...)
		if w.Code != http.StatusOK {
			t.Fatalf("traffic submit %s: %d %s", key, w.Code, w.Body.String())
		}
	}
	if w = request(a, "POST", "/api/stations/shop/traffic", map[string]any{"action": "drain", "runId": runID}, admin...); w.Code != http.StatusOK {
		t.Fatalf("traffic drain: %d %s", w.Code, w.Body.String())
	}
}
