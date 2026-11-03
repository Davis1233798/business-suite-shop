import test from 'node:test';
import assert from 'node:assert/strict';
import { createSqliteD1 } from '../../lib/db/sqlite-d1.ts';
import { setCustomD1 } from '../../db/index.ts';
import { exportWorkspace, exportWorkspaceCsv, restoreWorkspace, validateWorkspaceBackup } from '../../lib/tenant-backup.ts';
import type { TenantContext } from '../../lib/tenant.ts';

const context:TenantContext={organizationId:'org_shop_test',userId:'admin_test',userEmail:'admin@example.test',userDisplayName:'Admin',role:'admin',requestId:'backup-test'};
test('SHOP backup and restore use only repository tables', async () => {
 const d1=createSqliteD1();setCustomD1(d1);
 try {
  const snapshot=await exportWorkspace(context);
  const names=Object.keys(snapshot.tables).sort();
  const actual=d1._rawDb.prepare("SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'shop_%' ORDER BY name").all().map((row:{name:string})=>row.name);
  assert.ok(names.length>0);assert.deepEqual(names,actual);
  await restoreWorkspace(context,snapshot);
  await assert.rejects(()=>exportWorkspaceCsv(context,'other_system_table'));
  assert.throws(()=>validateWorkspaceBackup(context,{...snapshot,tables:{...snapshot.tables,other_system_table:[]}}));
 } finally { d1._rawDb.close(); }
});
