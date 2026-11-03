# HTTP API 契約

目前展示版，同源 JSON API；除另列 GET 外均使用 POST、`Content-Type: application/json`。金額為整數 TWD，數量為整數。回應格式：

```json
{"success":true,"data":{},"request_id":"UUID"}
```

```json
{"success":false,"error":{"code":"OPERATION_FAILED","message":"可讀錯誤"},"request_id":"UUID"}
```

讀取資料後使用回傳的記錄 ID，不應自行拼接種子 ID。一般輸入／業務錯誤回 400、明確角色拒絕回 403、載入失敗可能為 500，健康檢查失敗為 503。部分舊路由仍把服務層權限錯誤包成 400，客戶端須同時檢查 `success` 與 `error`，不能只依狀態碼判斷成功。request_id 用於對照該次 API 結果。

瀏覽器須保留 `demo_session_id` cookie。middleware 在缺少或失效時建立新空間，第一個 API 自動 seed；外部呼叫需保留 Cookie jar 才能在同一空間操作。`demo_user_role=admin|operator|readonly` 僅是 sandbox 角色選擇，非正式驗證。傳入組織／使用者／角色 header 不會授權跨空間存取。

## 查詢與通用

| GET 路徑 | 回傳 |
|---|---|
| `/api/health` | `status`, `database`, `payment`；檢查 DB 連線，不代表第三方服務可用 |
| `/api/mes/data` | 工單、製程、BOM、工作中心、物料與品質／OEE 等展示資料 |
| `/api/erp/data` | 主檔、採購／銷售及明細、庫存、發票、試算表 |
| `/api/pos/data` | 門市、收銀機、班次、商品、交易／明細、掛單、日報 |
| `/api/crm/data` | 客戶、聯絡人、潛客、階段、商機、活動、工單、漏斗、報價／明細、同意紀錄與角色 |
| `/api/dining/data` | 桌位／桌次、菜單／加料、訂單／明細、KDS 與預約 |
| `/api/shop/data` | 商品／規格、分類、訂單／明細、退貨、可見評價、角色與淨營收 |

`GET /api/shop/data` 查詢參數：`q` 商品名稱、`category` 分類 ID、`min`／`max` 價格、`page`（預設 1）、`management=1`（僅 admin 得全部訂單；其他情況僅自己的訂單）。商品每頁最多 50，分類／名稱先篩選分頁，規格價格在該頁內篩選；沒有保證精確總筆數。

`POST /api/seed/reset` 無 request body，僅 admin。清除自己的領域資料後重新 seed，其他訪客空間不受影響。

## Go/React Shop checkout migration slice

以下路徑屬於 Go/React station runtime，使用 `suite_session` 登入工作區；需要啟用的 Shop grant 和可寫角色。售價、組織、身份、庫存與訂單金額都由伺服器決定。

`POST /api/stations/shop/checkout`：`items:[{variantId,quantity}]`（1–50 種規格，各 1–999 件），或為舊用戶保留的 `variantId` + `quantity`（1–100），另含 `idempotencyKey`, `customerEmail`, `shippingAddress:{name,city,address,phone}` 及可選 `couponCode`。售價、租戶、折扣和應付金額均由伺服器計算；優惠券按該租戶的啟用狀態、到期時間和商品小計最低門檻驗證。百分比沿用 legacy 四捨五入，固定/百分比折扣都不會超過商品小計；一張券只適用一次且不與其他券併用。運費沿用 legacy 固定 TWD 60，沒有免運門檻。商品折扣依行小計比例分配，尾行吸收整數餘數，訂單明細持久化折後金額，退款依明細折後金額與退貨數量比例計算。優惠券不設核銷次數上限（既有 promotion schema 無此欄位）。以可用量 `stock_quantity - reserved_quantity` 扣庫，建立 Shop 訂單／明細及 15 分鐘預留；相同租戶、帳號、購物車、地址和券碼的 key 重送回原訂單；不同 payload、券碼或帳號重用 key 回 409。過期訂單的舊 key 回 409，需重新結帳並使用新 key。預留到期目前由後續 checkout 請求釋放。

`POST /api/stations/shop/checkout-lab` 僅供後台可寫角色：`{action:"prepare",capacity}` 建立 1–1000 件庫存的隔離 run；`{action:"checkout",runId,key}` 透過同一 checkout transaction 模擬並行買家。`GET /api/stations/shop/checkout-lab?runId=...` 回傳租戶範圍內的持久化訂單、明細、庫存與金額不變式，後台唯讀角色可讀取。每 run 使用暫存 SQLite DB；最多每組織 10 個、全站 100 個，30 分鐘後由下一次 lab 請求清理。付款回呼、付款記錄、出貨與退款仍未移植到 Go。

## MES

`POST /api/operations/mes` 以 `action` 分派：

| action | JSON 欄位 |
|---|---|
| `create` | `productId`, `targetQuantity` 正整數；綁定最新有效 BOM 與預設工作中心 |
| `bom` | `productId`, `materialSku`, `materialName`, `quantityPerUnit` 正整數；新增下一版本單項物料 BOM |
| `status` | `workOrderId`, `status`: `released`／`in_progress`／`closed`；下達須草稿、開工須備料、結案須完工且品檢通過 |
| `material` | `workOrderId`, `materialLotId`, `issuedQty` 正整數、`returnedQty` 非負整數 |
| `quality` | `workOrderId`, `sampleQuantity` 正整數、`failedQuantity` 非負整數、可選 `notes` |
| `downtime` | `workCenterId`, `startTime`, `endTime`, `reason`；時間為無 offset 的台北 `YYYY-MM-DDTHH:mm` |

`POST /api/mes/report`：`workOrderId`, `operationId`, `goodQuantity`, `scrapQuantity`, 可選 `notes`, `idempotencyKey`。回傳本次報工及累計完成量；客戶端對同一操作重試應保留相同 key。

## ERP

`POST /api/operations/erp`：

| action | JSON 欄位 |
|---|---|
| `item` | `sku`, `name`, `boxConversionRatio` 正整數 |
| `purchase` | `supplierId`, `itemId`, `quantity`, `unitPrice`（兩者正整數）；建立採購單與單行明細 |
| `sale` | `customerId`, `itemId`, `quantity`, `unitPrice`（兩者正整數）；建立已確認銷售單 |
| `approve` | `poId`, `decision`: `approve`／`reject`，可選 `reason`（駁回須原因）；須管理員 |
| `receive` | `lineId` 採購明細、`warehouseId`, `quantity` 正整數；須先核准 |
| `ship` | `lineId` 銷售明細、`warehouseId`, `quantity` 正整數；庫存不足拒絕 |

`POST /api/erp/transfer`：`itemId`, `fromWarehouseId`, `toWarehouseId`, `quantity`。

`POST /api/erp/invoice/pay`：`invoiceId`, `amount`, 可選 `paymentMethod`。以服務端餘額核對，不得超收。

## POS

`POST /api/pos/checkout`：`storeId`, `registerId`, `shiftId`, `items:[{barcode,quantity,lineDiscount?}]`, `tenderAmount`, `paymentMethod`（現金或模擬付款）, `idempotencyKey`。售價由服務端決定；回傳 receipt／應付／找零，原子扣庫。相同 key 重試回同一交易。

`POST /api/pos/actions`：

| action | JSON 欄位 |
|---|---|
| `open` | `registerId`, `openingCash` 非負整數 |
| `close` | `shiftId`, `closingCash` 非負整數、可選 `notes`；差異須說明 |
| `hold` | `storeId`, `cartName`, `items:[{barcode,quantity,lineDiscount}]`；掛單不扣庫 |
| `retrieve` | `cartId`；排他取回 |
| `receipt` | `receiptNumber`；重印不重扣庫款 |
| `refund` | `saleId`, `lineReturns:[{lineId,quantity}]`, `reason`；僅 admin |

另保留 `POST /api/pos/refund` 舊端點，使用同一服務退款；新客戶端使用 `/actions`。

## CRM

`POST /api/crm/lead/convert`：`leadId`, 可選 `dealAmount` 非負整數、`closeDate`。原子建立客戶、聯絡人與商機，已轉換時回相同關聯。

`POST /api/crm/opportunity/stage`：`opportunityId`, `stageId`, 可選 `wonReason`, `lossReason`；成交／失單須相應原因。

`POST /api/crm/action`：

| action | JSON 欄位 |
|---|---|
| `contact` | `name`, `email`, 可選 `accountId`, `phone`, `title`, `marketingConsent`；預設不同意，email 去空白／小寫查重 |
| `revokeConsent` | `contactId`；撤回後立即不在行銷收件名單 |
| `activity` | `subject`, `dueDate` 完整 ISO 時間，可選 `activityType`: `task`／`call`／`meeting`／`email` |
| `completeActivity` | `activityId`；重送不重設完成時間 |
| `quote` | `opportunityId`, `itemName`, `quantity` 正整數、`unitPrice` 非負整數；建立下一版本送審快照 |
| `approveQuote` | `quoteId`；僅 admin、核准後鎖定 |
| `ticket` | `subject`, 可選 `customerId`, `contactId`, `priority`: `low`／`medium`／`high`／`urgent`，`slaHours` 1–720（預設 24） |

工單 `dueAt` 為 UTC ISO，依台北週一至五 09:00–18:00 計算；查詢回傳即時計算的逾期旗標，尚未扣除國定假日。

## 餐飲

| POST 路徑 | JSON 欄位 |
|---|---|
| `/api/dining/session` | `tableId`, `guestCount`；空桌且不超容量才能開桌，回傳 `sessionToken` |
| `/api/dining/reservation` | `tableId`, `customerName`, `customerPhone`, `guestCount`, `reservationTime` 完整 ISO offset |
| `/api/dining/order` | `sessionToken`, `items:[{menuItemId,quantity,modifiers?:[{groupId,optionId}]}]`, `idempotencyKey`；服務端依菜單／加料定價 |
| `/api/dining/kds/status` | `ticketId`, `newStatus`: `cooking`／`done`／`served`；只能依狀態機前進 |
| `/api/dining/cancel` | `orderItemId`, `reason`；已結帳不可退菜 |
| `/api/dining/bill/pay` | `sessionId`, `splits:[{splitIndex,amount,paymentMethod}]`；paymentMethod 為 `cash`／`mock_card`，總額須符合服務端計算的含 10% 服務費應付 |

桌次結帳後 token 失效，不可重複付款。分單為金額拆分，尚非逐行分配支付。

## 電商

建議使用 `POST /api/shop/action`：

| action | JSON 欄位 |
|---|---|
| `checkout` | `customerEmail`, `shippingAddress:{name,city,address,phone}`, `items:[{variantId,quantity}]`, 可選 `couponCode`, `idempotencyKey` |
| `payment` | `orderId`, `attemptReference`, `amount`, `status`: `success`／`failed`；純模擬付款，金額須等於訂單 |
| `ship` | `orderId`, 可選 `carrier`；僅 admin，已付款整單出貨，回傳模擬追蹤碼 |
| `refund` | `orderId`, `orderItemId`, `quantity`, `reason`；僅 admin，退原單商品、按折扣後商品額退款及回庫 |
| `review` | `productId`, `orderId`, `rating` 1–5，可選 `comment`；必須自己的已付款訂單買過該商品且未重複評價 |

服務端固定運費 60，忽略客戶端運費／商品售價。待付庫存於建單時扣減並建立 15 分鐘 reservation；商品查詢／結帳／付款請求觸發逾時回庫；成功付款後 reservation 為 consumed。退款商品額不自動退運費。

舊 TypeScript 端點仍保留於原 runtime。Go Shop 新付款端點為 `/api/stations/shop/payment/callback`，僅供已登入且具 Shop back-office 寫入權限的使用者操作；它模擬付款結果，不驗證外部 provider 簽章、不實際扣款。成功會原子提交付款嘗試、訂單狀態、Go 結帳預留消耗與 `shop.paid` outbox 事件。

## 尚未公開的能力

`lib/backup-restore.ts` 保留為舊的跨系統實驗工具；它依賴三套系統資料表，無法在目前單站資料庫運作，不應作為備份或還原方案。`lib/export-csv.ts` 有 CSV 轉義工具，尚無報表匯出 API。Outbox 僅預留資料表，沒有對外事件交付契約。


SHOP 本機 Vinext SQLite 可透過 `node scripts/shop-snapshot.mjs backup SOURCE NEW_TARGET --confirm-plaintext` 建立完整快照；還原以 `restore` 指令複製快照到不存在的新路徑，再於停止服務後切換資料庫。工具使用 SQLite 一致性快照與完整性檢查，不覆蓋既有檔案。快照包含全部租戶及身分、稽核資料，且**未加密**，必須由有權存取本機檔案的人員保護；不適用 SQLCipher 桌面資料庫或 Cloudflare D1。

快照先寫在目標目錄內的隨機暫存檔，驗證後以不覆蓋的原子硬連結發布；檔案系統不支援硬連結時會失敗。若程式遭強制中止，可能留下 `.suite-snapshot-*.tmp` 明文暫存檔，應由具權限的人員確認後安全刪除；Windows 請以資料夾 ACL 限制存取。
