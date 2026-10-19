const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const {startServer} = require('./server.cjs');
test('static server serves local assets and rejects invalid methods and Host', async () => {
 const root=await fs.mkdtemp(path.join(os.tmpdir(),'gitchain-client-'));
 await fs.writeFile(path.join(root,'index.html'),'<title>GitChain</title>');
 const server=await startServer(root,0);
 try {
  const url=`http://127.0.0.1:${server.address().port}`;
  const ok=await fetch(url); assert.equal(ok.status,200); assert.match(await ok.text(),/GitChain/);
  assert.match(ok.headers.get('content-security-policy'),/object-src 'none'/);
  assert.equal((await fetch(url,{method:'POST'})).status,405);
  const badHost = await new Promise((resolve,reject)=>{const req=require('node:http').get(url,{headers:{Host:'evil.example'}},res=>{res.resume();resolve(res.statusCode);});req.on('error',reject);}); assert.equal(badHost,403);
  assert.equal((await fetch(url+'/missing')).status,404);
  assert.equal((await fetch(url+'/%2e%2e%2fpackage.json')).status,404);
 } finally {await new Promise(resolve=>server.close(resolve));await fs.rm(root,{recursive:true});}
});
