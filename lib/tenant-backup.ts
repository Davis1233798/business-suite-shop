import { eq, getTableColumns, getTableName, sql } from "drizzle-orm";
import * as schema from "../db/schema";
import { getDb } from "../db";
import { TenantContext, checkPermission } from "./tenant";
import { generateCsv } from "./export-csv";

// Identity, authorization, audit history and replay caches are intentionally not
// restorable business data. Audit history survives restoring a snapshot.
const excluded = new Set(["organizations", "users", "memberships", "audit_events", "idempotency_keys", "outbox_events", "demo_seed_states"]);
const tables = Object.values(schema).filter(table => "organizationId" in getTableColumns(table) && getTableName(table).startsWith("shop_") && !excluded.has(getTableName(table)));
const byName = new Map<string, (typeof tables)[number]>(tables.map(table => [getTableName(table), table]));
export interface TenantBackup { format: "business-suite/v1"; organizationId: string; exportedAt: string; tables: Record<string, Record<string, any>[]>; }

export async function exportWorkspace(context: TenantContext): Promise<TenantBackup> {
  const db = getDb();
  const queries = tables.map(table => db.select().from(table).where(eq((getTableColumns(table) as any).organizationId, context.organizationId)).orderBy(sql`rowid`));
  const results = await db.batch(queries as any);
  return { format: "business-suite/v1", organizationId: context.organizationId, exportedAt: new Date().toISOString(), tables: Object.fromEntries(tables.map((table, index) => [getTableName(table), results[index]])) as TenantBackup["tables"] };
}

const referenceTargets: Record<string, string> = {
  mes_productId: "mes_products", mes_bomVersionId: "mes_bom_versions", mes_workOrderId: "mes_work_orders", mes_workCenterId: "mes_work_centers", mes_materialLotId: "mes_material_lots", mes_operationId: "mes_operations", mes_reportId: "mes_production_reports",
  erp_itemId: "erp_items", erp_warehouseId: "erp_warehouses", erp_supplierId: "erp_suppliers", erp_customerId: "erp_customers", erp_poId: "erp_purchase_orders", erp_receiptId: "erp_receipts", erp_soId: "erp_sales_orders", erp_shipmentId: "erp_shipments", erp_invoiceId: "erp_invoices", erp_journalId: "erp_journal_entries",
  pos_storeId: "pos_stores", pos_registerId: "pos_registers", pos_shiftId: "pos_shifts", pos_saleId: "pos_sales", pos_originalSaleId: "pos_sales", pos_itemId: "pos_catalog_items", pos_saleLineId: "pos_sale_lines", pos_refundId: "pos_refunds",
  crm_accountId: "crm_accounts", crm_contactId: "crm_contacts", crm_primaryContactId: "crm_contacts", crm_stageId: "crm_stages", crm_opportunityId: "crm_opportunities", crm_quoteId: "crm_quotes", crm_convertedAccountId: "crm_accounts", crm_convertedContactId: "crm_contacts", crm_convertedOpportunityId: "crm_opportunities",
  dining_locationId: "dining_locations", dining_tableId: "dining_tables", dining_groupId: "dining_modifier_groups", dining_sessionId: "dining_table_sessions", dining_orderId: "dining_orders", dining_orderItemId: "dining_order_items", dining_menuItemId: "dining_menu_items", dining_billId: "dining_bills",
  shop_categoryId: "shop_categories", shop_productId: "shop_products", shop_variantId: "shop_variants", shop_cartId: "shop_carts", shop_orderId: "shop_orders", shop_orderItemId: "shop_order_items",
};

export function validateWorkspaceBackup(context: TenantContext, value: unknown): TenantBackup {
  const backup = value as TenantBackup;
  if (!backup || backup.format !== "business-suite/v1" || backup.organizationId !== context.organizationId || !backup.tables || typeof backup.tables !== "object") throw new Error("只能還原同一展示空間匯出的完整 v1 備份");
  const names = Object.keys(backup.tables);
  if (names.length !== tables.length || names.some(name => !byName.has(name))) throw new Error("備份資料表清單不完整或含未知資料表");
  let rowCount = 0;
  for (const [name, rows] of Object.entries(backup.tables)) {
    if (!Array.isArray(rows)) throw new Error(`${name} 必須為資料列陣列`);
    rowCount += rows.length;
    if (rowCount > 10000) throw new Error("備份最多支援 10,000 筆展示資料");
    const columns = getTableColumns(byName.get(name)!) as Record<string, any>;
    const seenIds = new Set<string>();
    for (const row of rows) {
      if (!row || typeof row !== "object" || Array.isArray(row) || row.organizationId !== context.organizationId) throw new Error(`${name} 含其他組織或無效資料列`);
      if (Object.keys(row).length !== Object.keys(columns).length || Object.keys(row).some(key => !(key in columns))) throw new Error(`${name} 欄位不完整或未知`);
      for (const [key, column] of Object.entries(columns)) {
        const field = row[key];
        if (field === null) { if (column.notNull) throw new Error(`${name}.${key} 不可為空`); continue; }
        if (column.dataType === "number" && (!Number.isSafeInteger(field))) throw new Error(`${name}.${key} 必須為整數`);
        if (column.dataType === "boolean" && typeof field !== "boolean") throw new Error(`${name}.${key} 必須為布林值`);
        if (column.dataType === "string" && (typeof field !== "string" || field.length > 100000)) throw new Error(`${name}.${key} 文字格式無效`);
      }
      if (typeof row.id !== "string" || !row.id || seenIds.has(row.id)) throw new Error(`${name} 含空白或重複 ID`);
      seenIds.add(row.id);
      if ("stockQuantity" in row && (row.stockQuantity < 0 || row.reservedQuantity > row.stockQuantity)) throw new Error(`${name} 庫存不合法`);
      if (name === "mes_work_orders" && (row.completedQuantity < 0 || row.scrappedQuantity < 0 || row.completedQuantity + row.scrappedQuantity > row.targetQuantity)) throw new Error("工單報工量不合法");
      if (name === "erp_invoices" && (row.paidAmount < 0 || row.paidAmount > row.totalAmount)) throw new Error("帳單已付金額不合法");
      if (name === "pos_sale_lines" && (row.refundedQuantity < 0 || row.refundedQuantity > row.quantity)) throw new Error("退款數量不合法");
    }
  }
  const ids = new Map(Object.entries(backup.tables).map(([name, rows]) => [name, new Set(rows.map(row => row.id))]));
  for (const [name, rows] of Object.entries(backup.tables)) {
    const prefix = name.split("_")[0];
    for (const row of rows) for (const [key, field] of Object.entries(row)) {
      const target = name === "shop_inventory_reservations" && key === "cartId" ? "shop_orders" : referenceTargets[`${prefix}_${key}`];
      if (target && field !== null && !ids.get(target)?.has(field)) throw new Error(`${name}.${key} 找不到同一備份內的來源資料`);
    }
  }
  return backup;
}

export async function restoreWorkspace(context: TenantContext, value: unknown) {
  if (!checkPermission(context, "admin").allowed) throw new Error("403 Forbidden");
  const backup = validateWorkspaceBackup(context, value);
  const db = getDb();
  const queries: any[] = tables.map(table => db.delete(table).where(eq((getTableColumns(table) as any).organizationId, context.organizationId)));
  // Insertion order preserves ledger row order, which its consistency trigger validates.
  for (const table of tables) for (const row of backup.tables[getTableName(table)]) queries.push(db.insert(table).values(row as any));
  queries.push(db.delete(schema.idempotencyKeys).where(eq(schema.idempotencyKeys.organizationId, context.organizationId)));
  queries.push(db.insert(schema.auditEvents).values({ id: crypto.randomUUID(), organizationId: context.organizationId, actorId: context.userId, actorRole: context.role, action: "RESTORE_WORKSPACE", entityType: "WORKSPACE", entityId: context.organizationId, afterValue: JSON.stringify({ exportedAt: backup.exportedAt }), reason: "管理員還原自己的展示空間備份", requestId: context.requestId }));
  await db.batch(queries as any);
  return { restoredTables: tables.length, restoredRows: Object.values(backup.tables).reduce((sum, rows) => sum + rows.length, 0) };
}

export async function exportWorkspaceCsv(context: TenantContext, name: string) {
  const table = byName.get(name);
  if (!table) throw new Error("不支援的報表資料表");
  const columns = getTableColumns(table) as Record<string, any>;
  const rows = await getDb().select().from(table).where(eq(columns.organizationId, context.organizationId)).orderBy(sql`rowid`);
  const headers = Object.keys(columns);
  return "\uFEFF" + generateCsv(headers, rows.map(row => headers.map(key => (row as any)[key])));
}
