import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { DatabaseSync } from "node:sqlite";
import { createSqliteD1 } from "../../lib/db/sqlite-d1.ts";
import { snapshot } from "../../scripts/shop-snapshot.mjs";

test("offline full SHOP snapshot captures WAL and restores to a new file without overwriting", () => {
 const dir = fs.mkdtempSync(path.join(os.tmpdir(), "shop-snapshot-"));
 let db: DatabaseSync | undefined;
 try {
  const source = path.join(dir,"source.sqlite"), backup = path.join(dir,"backup.sqlite"), restored = path.join(dir,"restored.sqlite");
  db = createSqliteD1(source)._rawDb;
  db!.exec("INSERT INTO organizations(id,name,slug) VALUES ('one','Original','one'),('two','Second','two')");
  snapshot(source,backup);
  db!.exec("UPDATE organizations SET name='Changed' WHERE id='one'");
  snapshot(backup,restored);
  const check = new DatabaseSync(restored,{readOnly:true});
  assert.equal(check.prepare("SELECT name FROM organizations WHERE id='one'").get()?.name,"Original");
  assert.equal(check.prepare("SELECT COUNT(*) AS n FROM organizations").get()?.n,2);
  check.close();
  const originalBackup = fs.readFileSync(backup);
  assert.throws(() => snapshot(source,backup), /EEXIST/);
  assert.deepEqual(fs.readFileSync(backup), originalBackup);
  assert.equal(fs.readdirSync(dir).filter(name => name.startsWith(".suite-snapshot-")).length, 0);
  assert.equal(db!.prepare("SELECT name FROM organizations WHERE id='one'").get()?.name,"Changed");
  const bad=path.join(dir,"bad.sqlite"), rejected=path.join(dir,"rejected.sqlite");
  const foreign=new DatabaseSync(bad); foreign.exec("CREATE TABLE foreign_data (value TEXT)"); foreign.close();
  assert.throws(() => snapshot(bad,rejected)); assert.equal(fs.existsSync(rejected),false);
  assert.equal(fs.readdirSync(dir).filter(name => name.startsWith(".suite-snapshot-")).length, 0);
 } finally { db?.close(); fs.rmSync(dir,{recursive:true,force:true}); }
});
