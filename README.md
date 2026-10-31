# MarketLane — 電商

這是 電商 的本地遷移 repo。首頁 `/` 與 `/shop` 指向本站，營運入口為 `/commerce`；其他系統的頁面與 API 已移除，但部分共用 schema、服務及 Go UI 程式仍保留其他領域，尚未完全解耦。

本 repo 的獨立 GitHub 遠端為 [business-suite-shop](https://github.com/Davis1233798/business-suite-shop)；正式部署尚未驗證，原公開站台尚未切換。功能是展示與遷移原型，不代表完整商用系統。

## 本地執行與測試

需要 Node.js 22.13 以上、npm 與 Go。

```powershell
npm run install:ci
npm run build
npm run dev
```

依終端機印出的網址開啟。Vinext Node SQLite 使用 `.data/shop-vinext.sqlite`，會初始化本站資料；Go 工作站另用 `.data/shop-repository.sqlite`，兩者資料不共用。

```powershell
npm test
npm run go:test
npm run web:build
$env:SUITE_WEB = (Resolve-Path web-dist).Path
go -C backend run .
```

Go 工作站可從 `http://127.0.0.1:8095/shop/login` 進入。Go 預設資料庫可用 `SUITE_DB` 指定；既有含 `organizations` 且缺少 `shop_repository_schema` 標記的資料庫會被拒絕。Vinext 本機資料庫也檢查本站標記。請使用新路徑，舊資料不會自動遷移。

本站 `npm test` 最近通過 23 項，Go 測試及建置亦通過；測試已裁剪為本站範圍，原跨系統測試留在作品集 repo，不能視為跨系統或正式部署驗收。Vinext 開發模式 HTTP smoke 已檢查本站、health 與他站拒絕，尚未涵蓋全部業務流程。

## D1 與部署

`drizzle/shop_d1_new_database.sql` 僅供**新空 D1** 初始化；Worker 缺少本站標記時回傳 503。本站 bootstrap 已在隔離的本地 Wrangler D1 執行，production bundle 的本地 Worker `/api/health` 回傳 200 且顯示資料庫連線；這不涵蓋遠端 D1、業務寫入或部署。新 D1 的雲端部署與資料移轉尚未驗證。不要套用至舊資料庫、覆蓋既有 hosting identity 或直接切換正式站；先完成備份、遷移、核對及回滾計畫。

## 限制

- 展示帳號及資料為測試用途，角色切換並非正式身分驗證或生產授權。
- 金流、物流、發票與通知等整合未作正式商用驗證；不要放入真實敏感資料。
- Go / React / Electron 工作站仍在遷移，交易功能不能等同原 Vinext 展示版；行動實機、列印及正式部署尚需驗證。
- 本機資料庫未提供靜態加密，請避免儲存敏感資料。`.data/`、`.wrangler/` 與建置輸出不應提交。
