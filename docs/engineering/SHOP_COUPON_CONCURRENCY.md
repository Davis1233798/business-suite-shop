# Shop Coupon Concurrency & Reverse Ledger Architecture

## 1. 概述 (Overview)

本文件定義 Business Suite Shop (MarketLane Commerce) P1 核心交易機制與不變量規範，涵蓋：
1. **優惠券折扣分攤與餘數分配守恆 (Coupon Discount Allocation - S04 & P02)**
2. **結帳並發防超賣與購物車保留超時 (Checkout Concurrency & Cart Reservation)**
3. **退貨退款反向台帳 (Return & Refund Reverse Ledger)**

嚴格遵循 `CONSTRUCTION.md`、`DEVELOP_PLAN.md` 及 `HARNESS.md` 的系統責任邊界、交易原子性與多租戶資料隔離標準。

---

## 2. 系統與交易邊界 (Transaction Boundaries)

Shop 擁有線上通路之訂單、商品規格、促銷優惠、購物車及履約退款台帳，與 ERP (LedgerWorks) 庫存及 POS (CounterPoint) 門市台帳保持嚴格領域隔離。所有資料寫入必須在單一資料庫交易 (Database Transaction) 內完成。

```
+---------------------------------------------------------------------------------------+
|                                Shop Transaction Boundary                              |
|                                                                                       |
|  [Checkout API]                                                                       |
|     |                                                                                 |
|     +---> 1. Validate Identity & Scope (Tenant isolation)                             |
|     +---> 2. Lock Idempotency Key (advisory / unique constraint)                      |
|     +---> 3. Price Products & Check Promotion (threshold, expiry, usage limit)       |
|     +---> 4. Proportional Discount Allocation (Integer remainder conservation)       |
|     +---> 5. Atomic Stock Deduction & Negative Check (stock_quantity >= reserved)     |
|     +---> 6. Atomic Coupon Quota Decrement (usage_limit - 1, times_used + 1)          |
|     +---> 7. Create Order & Items (with discounted line totals)                       |
|     +---> 8. Record Inventory Reservation (15-min TTL)                                |
|     +---> 9. Commit Idempotency Record (request hash & response body)                 |
|                                                                                       |
|  [Refund API]                                                                         |
|     |                                                                                 |
|     +---> 1. Verify Tenant & Admin Grant                                              |
|     +---> 2. Verify Order Payment Status == 'paid' & Target Order Item               |
|     +---> 3. Validate Cumulative Quantity <= Original Order Quantity                  |
|     +---> 4. Proportional Refund Calculation on Discounted Line Total                 |
|     +---> 5. Atomic Stock Replenishment (stock_quantity += refunded_qty)              |
|     +---> 6. Insert Return Record (status: 'refunded')                                |
|     +---> 7. Update Fulfillment Status (shipped -> shipped; unfulfilled -> hold/canc) |
|     +---> 8. Write Audit Event (`shop.refund.create`) & Outbox (`shop.refunded`)      |
|     +---> 9. Record Idempotency Replay State                                          |
+---------------------------------------------------------------------------------------+
```

---

## 3. 多租戶隔離 (Multi-Tenant Isolation)

每個請求在進入 API 層時，均由中介層解碼 session 並提取 `OrganizationID`：
- **實體隔離查詢**：所有 SQL 查詢（包含 `shop_variants`、`shop_promotions`、`shop_orders`、`shop_order_items`、`shop_returns`、`idempotency_keys`、`audit_events`、`outbox_events`）必須明確附帶 `organization_id = ?` 條件。
- **跨租戶防偽**：租戶 A 之優惠券不可被租戶 B 使用（報 409 Conflict）；租戶 B 亦無法查詢或退款租戶 A 之訂單（報 404 Not Found）；冪等鍵以 `(organization_id, key, endpoint)` 複合鍵隔離，杜絕跨租戶碰撞。

---

## 4. 核心狀態機與狀態轉移 (State Machines)

### 4.1 訂單付款狀態 (Payment Status)
```
          checkoutOrder()
 [None] ------------------> [pending]
                               |
            +------------------+------------------+
            | payment callback |                  | 15-min TTL expiry
            v                  |                  v
         [paid]                |              [expired]
            |                  |                  |
            | full refund      | payment failure  | (Stock restored)
            v                  v                  v
       [refunded]           [failed]            [end]
```

### 4.2 訂單履約狀態 (Fulfillment Status)
```
                       checkoutOrder()
 [None] ---------------------------------------------> [unfulfilled]
                                                            |
            +--------------------+--------------------------+
            | partial refund     | full merchandise refund  | shipOrder()
            v                    v                          v
      [return_hold]         [cancelled]                 [shipped]
            |                    |                          |
            | (blocks shipment)  | (blocks shipment)        | partial/full refund
            v                    v                          v
         [hold]                [end]                 [shipped] (remains shipped,
                                                      returns recorded)
```

---

## 5. 核心業務不變量 (Business Invariants)

### 5.1 優惠券折扣分攤與餘數守恆 (S04 & P02)
- **單一優惠券約束**：每筆訂單最多使用一張優惠券，不與其他優惠疊加。
- **門檻與有效性檢查**：
  - 優惠券必須處於啟用狀態 (`is_active = 1`)。
  - 當前時間必須早於過期時間 (`expires_at > now`)。
  - 商品原價總額 (merchandise subtotal) 必須達到最低消費金額 (`min_spend`)。
  - 總折抵金額上限不得超過商品原價總額（運費 NT$60 不列入折抵）。
- **整數餘數分攤演算法**：
  1. 每品項基礎折扣：`share_i = floor(rawLineTotal_i * discount / subtotal)`。
  2. 計算累計差額：`remaining = discount - sum(share_i)`。
  3. 餘數補正：從末位品項倒序向前遍歷，將 `remaining` 逐一攤入尚有餘額之品項，嚴格禁止零元品項 (`lineTotal == 0`) 被攤成負數。
  4. 最終分攤總和 `sum(allocated_discount_i) == discount`，持久化之品項折後金額總和嚴格等於 `subtotal - discount`。
- **使用次數配額原子控制**：
  - 若優惠券設定 `usage_limit`，下單交易內原子執行：
    ```sql
    UPDATE shop_promotions 
    SET times_used = times_used + 1,
        usage_limit = CASE WHEN usage_limit IS NOT NULL THEN usage_limit - 1 ELSE usage_limit END
    WHERE organization_id = ? AND code = ? AND is_active = 1
      AND (usage_limit IS NULL OR usage_limit > 0)
    ```
  - 若 `RowsAffected == 0`，代表配額已耗盡或並發爭用失敗，整筆交易回滾並回傳 HTTP 409。

### 5.2 結帳並發防超賣與冪等保全
- **庫存原子扣減**：
  - 扣減條件：`UPDATE shop_variants SET stock_quantity = stock_quantity - ? WHERE id = ? AND stock_quantity - reserved_quantity >= ?`。
  - 若任一品項庫存不足（`RowsAffected != 1`），全單交易即刻 Rollback，回傳 HTTP 409 Conflict。
  - 資料庫觸發器 `shop_stock_nonnegative` 確保庫存永不為負數。
- **購物車保留與超時釋放**：
  - 下單成功建立 15 分鐘有效之 `shop_inventory_reservations`。
  - 超時未付款訂單由 `releaseExpiredShopReservations` 原子轉為 `expired`，並全數回補庫存。
- **Idempotency-Key 重送保全**：
  - 綁定 `request_hash = SHA256(userID + email + couponCode + sortedCanonicalItems + addressBytes)`。
  - 相同 key 重送且 hash 一致：直接回傳快取之訂單回應 (`duplicate: true`, HTTP 200)。
  - 相同 key 篡改 payload（例如修改數量或換用優惠券）：拒絕並回傳 HTTP 409 Conflict。

### 5.3 退貨退款反向台帳 (Reverse Ledger)
- **原始關聯驗證**：退貨退款請求必須指定有效已付款訂單 (`payment_status == 'paid'`) 及原始 `order_item_id`。
- **退貨數量與金額上限**：
  - 累計退貨數量不得超過原始訂單品項數量：`alreadyReturned + requestedQty <= originalQuantity`。
  - 退款金額依該品項折後之 `line_total` 採累計整數比例退款：
    `refundAmount = floor(lineTotal * (alreadyReturned + requestedQty) / originalQty) - floor(lineTotal * alreadyReturned / originalQty)`。
  - 總退款額度嚴格守恆，不可超過該品項實收之折後金額，亦不可超過訂單未退金額。
- **反向台帳記錄**：
  - 庫存原子回補至 `shop_variants`。
  - 寫入審計軌跡 `audit_events` (`action = 'shop.refund.create'`)。
  - 寫入非同步發送任務 `outbox_events` (`event_type = 'shop.refunded'`)。
  - 若審計或 Outbox 寫入失敗，交易全數 Rollback。

---

## 6. 回滾與異常條件 (Rollback & Failure Conditions)

| 異常情境 | 觸發條件 | 處理機制 | HTTP 回應 |
|---|---|---|---|
| 庫存不足 | `stock_quantity - reserved_quantity < requested` | 交易回滾，無任何資料寫入 | 409 Conflict |
| 優惠券過期或停用 | `expires_at <= now` 或 `is_active == 0` | 交易回滾 | 409 Conflict |
| 優惠券未達門檻 | `subtotal < min_spend` | 交易回滾 | 409 Conflict |
| 優惠券次數超額 | `usage_limit <= 0` 或並發額滿 | 交易回滾 | 409 Conflict |
| 冪等鍵篡改 | 相同 key 但 `request_hash` 不符合 | 交易拒絕 | 409 Conflict |
| 超量退款 | `alreadyReturned + qty > originalQty` | 交易回滾 | 409 Conflict |
| 未付款退款 | `payment_status != 'paid'` | 交易回滾 | 409 Conflict |
| 審計或台帳故障 | 觸發器中斷或資料庫連線錯誤 | 交易回滾，保持原狀 | 503 Service Unavailable |
