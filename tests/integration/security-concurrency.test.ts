import test from "node:test";
import assert from "node:assert/strict";
import { NextRequest } from "next/server";
import { resolveTenantContext } from "../../lib/tenant";
import { getDb, runWithDatabase } from "../../db";
import { createSqliteD1 } from "../../lib/db/sqlite-d1";
import { seedOrganization } from "../../lib/seed";
import { organizations } from "../../db/schema";
import { eq } from "drizzle-orm";

test("Tenant sandbox ignores forged organization, user and admin headers", () => {
  const session = crypto.randomUUID();
  const request = new NextRequest("https://example.test/api/shop/data", { headers: { cookie: `demo_session_id=${session}; demo_user_role=readonly`, "x-organization-id": "victim", "x-user-id": "victim", "x-user-role": "admin" } });
  const context = resolveTenantContext(request);
  assert.equal(context.organizationId, `org_${session}`);
  assert.equal(context.role, "readonly");
  assert.notEqual(context.userId, "victim");
  assert.throws(() => resolveTenantContext(new NextRequest("https://example.test/api/shop/data")));
});

test("Request-scoped D1 bindings cannot leak between concurrent requests", async () => {
  const a = createSqliteD1(); const b = createSqliteD1();
  await Promise.all([
    runWithDatabase(a, async () => { await Promise.resolve(); await getDb().insert(organizations).values({ id: "a", name: "A", slug: "a" }); }),
    runWithDatabase(b, async () => { await Promise.resolve(); await getDb().insert(organizations).values({ id: "b", name: "B", slug: "b" }); }),
  ]);
  assert.equal((await runWithDatabase(a, () => getDb().select().from(organizations)))[0].id, "a");
  assert.equal((await runWithDatabase(b, () => getDb().select().from(organizations)))[0].id, "b");
});
