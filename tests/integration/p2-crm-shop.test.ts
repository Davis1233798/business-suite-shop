import test from "node:test";
import assert from "node:assert";
import { getDb } from "../../db/index.ts";
import { seedOrganization } from "../../lib/seed.ts";
import { ShopService } from "../../lib/services/shop-service.ts";
import { shopVariants, shopOrders, crmStages, shopOrderItems, shopProducts } from "../../db/schema.ts";
import { eq, and } from "drizzle-orm";

test("P2 Shop (MarketLane) Verification: S01 - S08", async () => {
  const orgId = `org_shop_test_${Date.now()}`;
  await seedOrganization(orgId);

  const adminCtx = {
    organizationId: orgId,
    userId: "usr_admin",
    userEmail: "admin@test.local",
    userDisplayName: "Shop Admin",
    role: "admin" as const,
    requestId: "req_shop_admin",
  };

  const buyerCtx = {
    ...adminCtx,
    userId: "usr_buyer_1",
    role: "operator" as const,
    requestId: "req_shop_buyer",
  };

  const db = getDb();

  // S01: 商品與規格搜尋
  const searchRes = await ShopService.searchProducts(buyerCtx, { page: 1, limit: 10 });
  assert.strictEqual(searchRes.products.length >= 1, true);
  const hoodie = searchRes.products[0];
  assert.strictEqual(hoodie.variants.length, 2);

  // S04: 100 x 2 打 9 折 = 180，加運費 60 = 240
  const varBlackM = hoodie.variants.find((v: any) => v.title.includes("M"))!;
  const checkout1: any = await ShopService.checkoutCart(buyerCtx, {
    sessionId: "sess_test_1",
    customerEmail: "buyer1@test.local",
    shippingAddress: { name: "王先生", city: "新北市", address: "板橋區文化路1號", phone: "0911-222-333" },
    items: [{ variantId: varBlackM.id, quantity: 2 }],
    couponCode: "SAVE10", // 10% off
    shippingFee: 60,
  });
  assert.strictEqual(checkout1.subtotal, 200);
  assert.strictEqual(checkout1.discountAmount, 20);
  assert.strictEqual(checkout1.shippingFee, 60);
  assert.strictEqual(checkout1.totalAmount, 240);

  // S04: 過期券拒絕
  await assert.rejects(
    async () => {
      await ShopService.checkoutCart(buyerCtx, {
        sessionId: "sess_test_expired",
        customerEmail: "buyer@test.local",
        shippingAddress: { name: "王先生", city: "新北市", address: "板橋區文化路1號", phone: "0911-222-333" },
        items: [{ variantId: varBlackM.id, quantity: 1 }],
        couponCode: "EXPIRED50",
      });
    },
    /優惠券已過期/
  );

  // S03: 最後一件併發防超賣檢驗 (Variant L has stock = 1 in seed)
  const varBlackL = hoodie.variants.find((v: any) => v.title.includes("L"))!;
  // First checkout succeeds
  const firstBuyerOrder: any = await ShopService.checkoutCart(buyerCtx, {
    sessionId: "sess_buyer_a",
    customerEmail: "buyera@test.local",
    shippingAddress: { name: "買家A", city: "台北市", address: "信義路", phone: "0900" },
    items: [{ variantId: varBlackL.id, quantity: 1 }],
  });
  assert.ok(firstBuyerOrder.orderId);

  // Second checkout for the same last item -> MUST REJECT (S03)
  await assert.rejects(
    async () => {
      await ShopService.checkoutCart(buyerCtx, {
        sessionId: "sess_buyer_b",
        customerEmail: "buyerb@test.local",
        shippingAddress: { name: "買家B", city: "台中市", address: "台灣大道", phone: "0900" },
        items: [{ variantId: varBlackL.id, quantity: 1 }],
      });
    },
    /庫存不足/
  );

  // S05: 模擬金流回呼 (金額不符拒絕、正常回呼入帳、重複回呼冪等)
  // Amount mismatch -> reject
  await assert.rejects(
    async () => {
      await ShopService.simulatePaymentCallback(buyerCtx, {
        orderId: checkout1.orderId,
        attemptReference: "PAY_MOCK_001",
        amount: 999, // actual is 240
        status: "success",
      });
    },
    /金流回呼金額不相符/
  );

  // Valid payment callback
  const payRes = await ShopService.simulatePaymentCallback(buyerCtx, {
    orderId: checkout1.orderId,
    attemptReference: "PAY_MOCK_001",
    amount: 240,
    status: "success",
  });
  assert.strictEqual(payRes.paymentStatus, "paid");

  // Duplicate callback -> idempotent
  const dupPayRes = await ShopService.simulatePaymentCallback(buyerCtx, {
    orderId: checkout1.orderId,
    attemptReference: "PAY_MOCK_001",
    amount: 240,
    status: "success",
  });
  assert.strictEqual(dupPayRes.alreadyProcessed, true);

  // S06: 出貨履約 (未付款禁止出貨；已付可出貨)
  const shipRes = await ShopService.shipOrder(buyerCtx, { orderId: checkout1.orderId });
  assert.strictEqual(shipRes.status, "shipped");
  assert.ok(shipRes.trackingNumber);

  // Attempt ship on unpaid order -> reject (S06)
  await assert.rejects(
    async () => {
      await ShopService.shipOrder(buyerCtx, { orderId: firstBuyerOrder.orderId }); // unpaid
    },
    /未付款之訂單不得出貨/
  );

  // S07: 退貨退款 (超量退貨拒絕；退貨成功補回庫存)
  const orderItems = await db.select().from(shopOrderItems)
    .where(eq(shopOrderItems.orderId, checkout1.orderId));
  const oi = orderItems[0]; // quantity was 2

  // Return 1
  const retRes = await ShopService.refundOrderItem(adminCtx, {
    orderId: checkout1.orderId,
    orderItemId: oi.id,
    quantity: 1,
    reason: "尺寸不合換貨",
  });
  assert.strictEqual(retRes.refundedQty, 1);

  // Cumulative over-refund: original 2, returned 1, trying to return 2 -> reject (S07)
  await assert.rejects(
    async () => {
      await ShopService.refundOrderItem(adminCtx, {
        orderId: checkout1.orderId,
        orderItemId: oi.id,
        quantity: 2,
        reason: "超退",
      });
    },
    /超過可退貨數量拒絕/
  );

  // S08: 評價 (完成購買方可評價)
  const reviewRes = await ShopService.addReview(buyerCtx, {
    productId: hoodie.id,
    orderId: checkout1.orderId,
    rating: 5,
    comment: "剪裁立體，防潑水性能極佳！",
  });
  assert.strictEqual(reviewRes.rating, 5);

  // Unpaid order cannot review
  await assert.rejects(
    async () => {
      await ShopService.addReview(buyerCtx, {
        productId: hoodie.id,
        orderId: firstBuyerOrder.orderId,
        rating: 4,
      });
    },
    /完成購買之會員方可發表商品評價/
  );
});
