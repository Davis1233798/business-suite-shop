import {readFileSync,readdirSync} from 'node:fs';
import path from 'node:path';
import assert from 'node:assert/strict';
const root='dist/client/_next/static/chunks';
const files=readdirSync(root);
const links=files.filter(name=>/^link-.*\.js$/.test(name));
if (!links.length) console.log('PASS: no next/link consumer in this system build');
for (const name of links) {
  const consumer=readFileSync(path.join(root,name),'utf8');
  const dependencies=[...consumer.matchAll(/\.\/((?:vinext-)?navigation-[^/"`]+\.js)/g)].map(match=>match[1]);
  assert.ok(dependencies.length,'next/link lost its isolated navigation namespace');
  const provider=dependencies.map(file=>readFileSync(path.join(root,file),'utf8')).join('\n');
  for (const required of ['navigateClientSide','getPrefetchInterceptionContext','getPrefetchCache']) {
    assert.ok(provider.includes(required+':'),'Missing navigation namespace export: '+required);
  }
}
console.log('PASS: next/link loads the isolated navigation namespace with its required public ABI');
