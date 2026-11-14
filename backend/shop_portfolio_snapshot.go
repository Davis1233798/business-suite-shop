package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Portfolio layout observed on the shared SQLite database.
const portfolioShopLayout = `
audit_events:id:TEXT,organization_id:TEXT,actor_id:TEXT,actor_role:TEXT,action:TEXT,entity_type:TEXT,entity_id:TEXT,before_value:TEXT,after_value:TEXT,reason:TEXT,request_id:TEXT,created_at:TEXT
idempotency_keys:id:TEXT,organization_id:TEXT,key:TEXT,endpoint:TEXT,request_hash:TEXT,response_status:INTEGER,response_body:TEXT,created_at:TEXT
outbox_events:id:TEXT,organization_id:TEXT,event_type:TEXT,payload:TEXT,status:TEXT,retry_count:INTEGER,created_at:TEXT
shop_cart_items:id:TEXT,organization_id:TEXT,cart_id:TEXT,variant_id:TEXT,quantity:INTEGER,created_at:TEXT
shop_carts:id:TEXT,organization_id:TEXT,session_id:TEXT,user_id:TEXT,coupon_code:TEXT,created_at:TEXT
shop_categories:id:TEXT,organization_id:TEXT,name:TEXT,slug:TEXT,created_at:TEXT
shop_inventory_reservations:id:TEXT,organization_id:TEXT,variant_id:TEXT,session_id:TEXT,cart_id:TEXT,quantity:INTEGER,expires_at:TEXT,status:TEXT,created_at:TEXT
shop_order_items:id:TEXT,organization_id:TEXT,order_id:TEXT,variant_id:TEXT,product_name:TEXT,variant_title:TEXT,sku:TEXT,unit_price:INTEGER,quantity:INTEGER,line_total:INTEGER,created_at:TEXT
shop_orders:id:TEXT,organization_id:TEXT,order_number:TEXT,user_id:TEXT,customer_email:TEXT,shipping_address_json:TEXT,subtotal:INTEGER,discount_amount:INTEGER,shipping_fee:INTEGER,total_amount:INTEGER,payment_status:TEXT,fulfillment_status:TEXT,tracking_number:TEXT,idempotency_key:TEXT,created_at:TEXT
shop_payment_attempts:id:TEXT,organization_id:TEXT,order_id:TEXT,attempt_reference:TEXT,amount:INTEGER,provider:TEXT,status:TEXT,callback_payload:TEXT,created_at:TEXT
shop_products:id:TEXT,organization_id:TEXT,name:TEXT,slug:TEXT,description:TEXT,category_id:TEXT,is_active:INTEGER,created_at:TEXT
shop_promotions:id:TEXT,organization_id:TEXT,code:TEXT,description:TEXT,discount_type:TEXT,discount_value:INTEGER,min_spend:INTEGER,expires_at:TEXT,is_active:INTEGER,created_at:TEXT
shop_returns:id:TEXT,organization_id:TEXT,order_id:TEXT,order_item_id:TEXT,quantity:INTEGER,reason:TEXT,status:TEXT,refund_amount:INTEGER,created_at:TEXT
shop_reviews:id:TEXT,organization_id:TEXT,product_id:TEXT,user_id:TEXT,order_id:TEXT,rating:INTEGER,comment:TEXT,is_visible:INTEGER,created_at:TEXT
shop_shipments:id:TEXT,organization_id:TEXT,order_id:TEXT,tracking_number:TEXT,carrier:TEXT,status:TEXT,shipped_at:TEXT
shop_variants:id:TEXT,organization_id:TEXT,product_id:TEXT,sku:TEXT,title:TEXT,color:TEXT,size:TEXT,price:INTEGER,stock_quantity:INTEGER,reserved_quantity:INTEGER,is_active:INTEGER,created_at:TEXT
`

func validatePortfolioShopSnapshot(ctx context.Context, db *sql.DB) error {
	for _, entry := range strings.Split(strings.TrimSpace(portfolioShopLayout), "\n") {
		table, expected, _ := strings.Cut(entry, ":")
		var actual string
		err := db.QueryRowContext(ctx, `SELECT COALESCE(group_concat(name || ':' || upper(type), ','), '') FROM (SELECT name,type FROM pragma_table_info(?) ORDER BY cid)`, table).Scan(&actual)
		if err != nil || actual != expected {
			return fmt.Errorf("unsupported Portfolio SQLite layout for %s", table)
		}
	}
	return nil
}
