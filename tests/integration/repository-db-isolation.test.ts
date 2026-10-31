import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { createSqliteD1 } from "../../lib/db/sqlite-d1.ts";

test("new repository database is isolated and legacy database is preserved", () => {
 const dir = fs.mkdtempSync(path.join(os.tmpdir(), "suite-isolation-"));
 try {
  const fresh = path.join(dir, "fresh.sqlite");
  const db = createSqliteD1(fresh)._rawDb as DatabaseSync;
  assert.equal(db.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name=?").get("mes_products"), undefined);
  db.close();
  const reopen = createSqliteD1(fresh)._rawDb as DatabaseSync; reopen.close();
  const legacy = path.join(dir, "legacy.sqlite");
  const original = new DatabaseSync(legacy);
  original.exec("CREATE TABLE organizations(id TEXT); CREATE TABLE retained(value TEXT); INSERT INTO retained VALUES ('keep')"); original.close();
  assert.throws(() => createSqliteD1(legacy), /Refusing existing database/);
  const check = new DatabaseSync(legacy); assert.equal((check.prepare("SELECT value FROM retained").get() as any).value, "keep"); check.close();
 } finally { fs.rmSync(dir, {recursive:true,force:true}); }
});
