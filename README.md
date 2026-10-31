# MarketLane — 電商

這是 電商 的本地遷移 repo。首頁 `/` 與 `/shop` 指向本站，營運入口為 `/commerce`；其他系統的頁面與 API 已移除，但部分共用 schema、服務及 Go UI 程式仍保留其他領域，尚未完全解耦。

本 repo 的獨立 GitHub 遠端為 [business-suite-shop](https://github.com/Davis1233798/business-suite-shop)；正式部署尚未驗證，原公開站台尚未切換。功能是展示與遷移原型，不代表完整商用系統。

## 本地執行與測試

需要 Node.js 22.13 以上、npm 與 Go 1.26 以上。

```powershell
npm run install:ci
npm run build
# 僅限全新的空白本地 D1；不要對既有資料庫重跑
node --import ./scripts/sites-env.mjs ./node_modules/wrangler/bin/wrangler.js d1 execute DB --local --config dist/server/wrangler.json --persist-to .wrangler/state --file drizzle/shop_d1_new_database.sql
npm run dev
```

依終端機印出的網址開啟；若開發 Worker 所用的本機 D1 尚未初始化本站標記，會回傳 503。上述初始化與 dev 讀取已在隔離的 Git archive 測試（使用既有 node_modules，未在該測試中重新安裝依賴）；請勿對既有 D1 直接執行新庫 SQL。Node SQLite 後備模式使用 repo 根目錄的 `.data/shop-vinext.sqlite`；Worker 使用 D1 綁定，兩者資料不共用。以下以 `go -C backend run .` 執行時，Go 工作站的預設資料庫位於 `backend/.data/shop-repository.sqlite`，與 Vinext 資料不共用。

```powershell
npm test
npm run go:test
npm run web:build
$env:SUITE_WEB = (Resolve-Path web-dist).Path
go -C backend run .
```

Go 工作站可從 `http://127.0.0.1:8095/shop/login` 進入。Go 預設資料庫可用 `SUITE_DB` 指定；既有含 `organizations` 且缺少 `shop_repository_schema` 標記的資料庫會被拒絕。Vinext 本機資料庫也檢查本站標記。請使用新路徑，舊資料不會自動遷移。

本站 `npm test`、Go 測試及建置曾通過；測試已裁剪為本站範圍，原跨系統測試留在作品集 repo，不能視為跨系統或正式部署驗收。Vinext 開發模式 HTTP smoke 已檢查本站、health 與他站拒絕，尚未涵蓋全部業務流程。

## D1 與部署

`drizzle/shop_d1_new_database.sql` 僅供**新空 D1** 初始化；Worker 缺少本站標記時回傳 503。本站 bootstrap 已在隔離的本地 Wrangler D1 執行，production bundle 的本地 Worker `/api/health` 回傳 200 且顯示資料庫連線；並已執行本站一項代表性業務寫入及讀回；這不涵蓋遠端 D1、全部業務流程或部署。新 D1 的雲端部署與資料移轉尚未驗證。不要套用至舊資料庫、覆蓋既有 hosting identity 或直接切換正式站；先完成備份、遷移、核對及回滾計畫。

本機 Vinext SQLite 的離線整庫快照操作與限制見 [API 文件](docs/API.md)；快照含所有租戶的明文資料，不支援 D1 或 SQLCipher。

Outbox 派送器只處理電商事件，不派送其他系統事件；仍需另行設定及驗證接收端。

## 限制

- 展示帳號及資料為測試用途，角色切換並非正式身分驗證或生產授權。
- 金流、物流、發票與通知等整合未作正式商用驗證；不要放入真實敏感資料。
- Go / React / Electron 工作站仍在遷移，交易功能不能等同原 Vinext 展示版；行動實機、列印及正式部署尚需驗證。
- 本機資料庫未提供靜態加密，請避免儲存敏感資料。`.data/`、`.wrangler/` 與建置輸出不應提交。
