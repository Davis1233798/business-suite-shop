import test from "node:test";
import assert from "node:assert/strict";
import { createSqliteD1 } from "../../lib/db/sqlite-d1.ts";
import { setCustomD1 } from "../../db/index.ts";
import { runWithInfrastructure } from "../../lib/infrastructure.ts";
import { dispatchInfrastructureOutbox } from "../../lib/infrastructure-outbox.ts";

test("dispatcher leaves foreign system events untouched", async () => {
 const db = createSqliteD1(); setCustomD1(db);
 try {
  db._rawDb.prepare("INSERT INTO outbox_events(id,organization_id,event_type,payload) VALUES (?,?,?,?)").run("foreign", "org", "mes.work-order.changed", "{} ");
  const result = await runWithInfrastructure({INFRA_GATEWAY_URL:"https://invalid.example",INFRA_GATEWAY_TOKEN:"test"}, () => dispatchInfrastructureOutbox());
  assert.equal(result.sent, 0); assert.equal(result.mesSynced, 0);
  assert.equal(db._rawDb.prepare("SELECT status FROM outbox_events WHERE id='foreign'").get().status,"pending");
 } finally { db._rawDb.close(); }
});


test("dispatcher sends this system's event when configured", async () => {
 const db = createSqliteD1(); setCustomD1(db); const previous = globalThis.fetch; const urls: string[] = [];
 globalThis.fetch = async (url) => { urls.push(String(url)); return new Response("{}", {status: 200, headers:{"content-type":"application/json"}}); };
 try {
  db._rawDb.prepare("INSERT INTO outbox_events(id,organization_id,event_type,payload) VALUES (?,?,?,?)").run("local", "org", "shop.paid", JSON.stringify({orderId:"o"}));
  const result = await runWithInfrastructure({INFRA_GATEWAY_URL:"https://gateway.example",INFRA_GATEWAY_TOKEN:"test"}, () => dispatchInfrastructureOutbox());
  assert.equal(result.sent, 1); assert.ok(urls.some(url => url.endsWith("/commerce/jobs")));
 } finally { globalThis.fetch = previous; db._rawDb.close(); }
});
