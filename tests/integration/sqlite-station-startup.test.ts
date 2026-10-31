import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, rmdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';

test('six independent processes can initialize the same fresh SQLite volume without partial schema', async () => {
  const directory = mkdtempSync(path.join(tmpdir(),'suite-station-startup-'));
  const file = path.join(directory,'business.sqlite');
  const adapter = new URL('../../lib/db/sqlite-d1.ts',import.meta.url).href;
  const source = `import {createSqliteD1} from ${JSON.stringify(adapter)};
    const db=createSqliteD1(process.argv[1]);
    const columns=db._rawDb.prepare('PRAGMA table_info(shop_orders)').all();
    if(!columns.some(c=>c.name==='order_number'))throw new Error('Partial repository schema');
    const triggers=db._rawDb.prepare("SELECT count(*) AS n FROM sqlite_master WHERE type='trigger'").get();
    if(triggers.n < 1)throw new Error('Missing transactional guards');
    db._rawDb.close();`;
  try {
    const outcomes = await Promise.all(Array.from({length:6},()=>new Promise<{code:number|null;stderr:string}>((resolve,reject)=>{
      const child=spawn(process.execPath,['--input-type=module','-e',source,file]);
      let stderr='';
      child.stderr.on('data',chunk=>{stderr+=chunk;});
      child.on('error',reject);
      child.on('close',code=>resolve({code,stderr}));
    })));
    for (const outcome of outcomes) assert.equal(outcome.code,0,outcome.stderr);
  } finally {
    // Only these explicit files belong to this test; no recursive deletion.
    for (const suffix of ['', '-wal','-shm']) rmSync(file+suffix,{force:true});
    rmdirSync(directory);
  }
});
