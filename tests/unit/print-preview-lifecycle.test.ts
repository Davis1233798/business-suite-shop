import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';

const source=readFileSync(new URL('../../web/src/main.tsx',import.meta.url),'utf8');

test('print preview dataset is scoped to the open preview lifecycle',()=>{
 const start=source.indexOf('useEffect(()=>{if(!showPrint');
 const marker='},[showPrint,site,grant?.scope]);';
 const end=source.indexOf(marker,start);
 assert.notEqual(start,-1,'print preview effect exists');
 assert.notEqual(end,-1,'print preview effect has its dependencies');
 const effect=source.slice(start,end+marker.length);
 assert.match(effect,/!showPrint[\s\S]*setPrintData\(\{\}\);return/,'closed preview clears only the print dataset');
 assert.match(effect,/new AbortController\(\)/,'the request has a lifecycle controller');
 assert.match(effect,/view=back`,\{signal:controller\.signal\}/,'the request uses the lifecycle signal');
 assert.match(effect,/if\(!controller\.signal\.aborted\)setPrintData\(value\)/,'late success cannot repopulate closed state');
 assert.match(effect,/catch\(e=>\{if\(!controller\.signal\.aborted\)/,'abort errors do not update UI state');
 assert.match(effect,/return\(\)=>controller\.abort\(\)/,'close, scope change, and unmount abort the request');
});
