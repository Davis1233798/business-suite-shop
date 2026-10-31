import { eq } from "drizzle-orm";
import { getDb } from "../db/index";
import {
  organizations,
  demoSeedStates,
  users,
  memberships,
  shopCategories,
  shopProducts,
  shopVariants,
  shopPromotions,
  shopOrders,
  shopOrderItems,
} from "../db/schema";

// Fictional fixtures only: no real brands, personal phone numbers or delivery addresses.
export async function seedOrganization(orgId: string = "org_demo_default") {
  const db = getDb();

  // 1. Organization & Core Users
  const existingOrg = await db
    .select()
    .from(organizations)
    .where(eq(organizations.id, orgId))
    .limit(1);

  if (existingOrg.length === 0) {
    await db.insert(organizations).values({
      id: orgId,
      name: "示範企業 (Business Suite Enterprise)",
      slug: orgId,
    }).onConflictDoNothing();
  }

  // Users
  const adminUser = { id: `usr_${orgId}_admin`, email: "admin@suite.test", displayName: "示範管理員 (Admin)" };
  const operatorUser = { id: `usr_${orgId}_op`, email: "operator@suite.test", displayName: "示範作業員 / 現場主管" };
  const readonlyUser = { id: `usr_${orgId}_ro`, email: "guest@suite.test", displayName: "訪客 (唯讀)" };

  for (const u of [adminUser, operatorUser, readonlyUser]) {
    const exists = await db.select().from(users).where(eq(users.id, u.id)).limit(1);
    if (exists.length === 0) {
      await db.insert(users).values(u).onConflictDoNothing();
    }
  }

  // Memberships
  const memberRoles = [
    { userId: adminUser.id, role: "admin" },
    { userId: operatorUser.id, role: "operator" },
    { userId: readonlyUser.id, role: "readonly" },
  ];
  for (const m of memberRoles) {
    const memId = `mem_${orgId}_${m.userId}`;
    const exists = await db.select().from(memberships).where(eq(memberships.id, memId)).limit(1);
    if (exists.length === 0) {
      await db.insert(memberships).values({
        id: memId,
        organizationId: orgId,
        userId: m.userId,
        role: m.role,
      }).onConflictDoNothing();
    }
  }

  // ==========================================
  // 7. Commerce「MarketLane」Seed
  // ==========================================
  const catApparel = { id: `shop_cat_01_${orgId}`, organizationId: orgId, name: "極簡機能服飾", slug: "apparel" };
  const catAccessory = { id: `shop_cat_02_${orgId}`, organizationId: orgId, name: "都市生活配件", slug: "accessories" };
  await db.insert(shopCategories).values([catApparel, catAccessory]).onConflictDoNothing();

  const prodHoodie = {
    id: `shop_prd_01_${orgId}`,
    organizationId: orgId,
    name: "極簡防風連帽機能外套",
    slug: "minimalist-tech-hoodie",
    description: "採用防潑水三層透氣微孔科技面料，適合都會通勤與戶外輕機能穿搭。",
    categoryId: catApparel.id,
    isActive: true,
  };
  await db.insert(shopProducts).values(prodHoodie).onConflictDoNothing();

  const varBlackM = {
    id: `shop_var_01_${orgId}`,
    organizationId: orgId,
    productId: prodHoodie.id,
    sku: "ML-HD-BLK-M",
    title: "曜石黑 / M",
    color: "曜石黑",
    size: "M",
    price: 100, // $100 for easy test calculations matching S04 fixture
    stockQuantity: 20,
    reservedQuantity: 0,
    isActive: true,
  };
  const varBlackL = {
    id: `shop_var_02_${orgId}`,
    organizationId: orgId,
    productId: prodHoodie.id,
    sku: "ML-HD-BLK-L",
    title: "曜石黑 / L",
    color: "曜石黑",
    size: "L",
    price: 100,
    stockQuantity: 1, // Only 1 in stock for concurrency anti-oversell testing!
    reservedQuantity: 0,
    isActive: true,
  };
  await db.insert(shopVariants).values([varBlackM, varBlackL]).onConflictDoNothing();

  // Promotion: 10% off
  await db.insert(shopPromotions).values([
    {
      id: `shop_promo_01_${orgId}`,
      organizationId: orgId,
      code: "SAVE10",
      description: "秋季全站 9 折優惠券",
      discountType: "percentage",
      discountValue: 10,
      minSpend: 0,
      isActive: true,
    },
    {
      id: `shop_promo_02_${orgId}`,
      organizationId: orgId,
      code: "EXPIRED50",
      description: "已過期優惠券",
      discountType: "fixed",
      discountValue: 50,
      minSpend: 200,
      expiresAt: "2020-01-01 00:00:00",
      isActive: false,
    },
  ]).onConflictDoNothing();

  // Completed order
  const order1 = {
    id: `shop_ord_01_${orgId}`,
    organizationId: orgId,
    orderNumber: "ORD-202610-001",
    customerEmail: "buyer@sites.test",
    shippingAddressJson: JSON.stringify({ name: "示範收件人 A", city: "台北市", address: "示範路段 A（非實際地址）", phone: "DEMO-PHONE-004" }),
    subtotal: 200,
    discountAmount: 20,
    shippingFee: 60,
    totalAmount: 240,
    paymentStatus: "paid",
    fulfillmentStatus: "shipped",
    trackingNumber: "TRK-TW-888999",
  };
  await db.insert(shopOrders).values(order1).onConflictDoNothing();
  await db.insert(shopOrderItems).values({
    id: `shop_oi_01_${orgId}`,
    organizationId: orgId,
    orderId: order1.id,
    variantId: varBlackM.id,
    productName: prodHoodie.name,
    variantTitle: varBlackM.title,
    sku: varBlackM.sku,
    unitPrice: 100,
    quantity: 2,
    lineTotal: 200,
  }).onConflictDoNothing();

  await db.insert(demoSeedStates).values({ organizationId: orgId }).onConflictDoNothing();
  return { success: true, organizationId: orgId };
}
