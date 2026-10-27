const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const MIME = { '.html':'text/html; charset=utf-8', '.js':'text/javascript', '.css':'text/css', '.svg':'image/svg+xml', '.json':'application/json' };
// Extra chain RPC origins the page may call (the independent verifier reads a
// remote chain). Only exact https/wss origins are accepted, never wildcards or
// paths, so a typo cannot open the policy to arbitrary hosts.
function parseRpcOrigins(list) {
  if (!list) return [];
  return String(list).split(',').map(s => s.trim()).filter(Boolean).map(s => {
    let u;
    try { u = new URL(s); } catch { throw Error(`GITCHAIN_RPC_ORIGINS: not a URL: ${s}`); }
    if (!['https:', 'wss:'].includes(u.protocol) || !/^[a-z0-9.-]+$/i.test(u.hostname) || u.origin !== s.replace(/\/$/, '')) throw Error(`GITCHAIN_RPC_ORIGINS: use exact https:// or wss:// origins only: ${s}`);
    return u.origin;
  });
}
function startServer(root, port = 5174, rpcOrigins = []) {
  root = path.resolve(root);
  const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self' http://127.0.0.1:* http://localhost:* ws://127.0.0.1:* ws://localhost:*" +
    rpcOrigins.map(o => ' ' + o).join('') + "; object-src 'none'; base-uri 'none'; frame-ancestors 'none'";
  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      if (req.headers.host !== `127.0.0.1:${server.address().port}`) { res.writeHead(403).end(); return; }
      if (!['GET','HEAD'].includes(req.method)) { res.writeHead(405).end(); return; }
      let pathname;
      try { pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname); }
      catch { res.writeHead(400).end(); return; }
      const file = path.resolve(root, '.' + (pathname === '/' ? '/index.html' : pathname));
      if (!file.startsWith(root + path.sep) || !fs.existsSync(file) || !fs.statSync(file).isFile()) { res.writeHead(404).end(); return; }
      res.setHeader('Content-Type', MIME[path.extname(file)] || 'application/octet-stream');
      res.setHeader('X-Content-Type-Options', 'nosniff');
      res.setHeader('Cache-Control', 'no-store');
      res.setHeader('Content-Security-Policy', csp);
      if (req.method === 'HEAD') { res.end(); return; }
      const stream = fs.createReadStream(file); stream.on('error', () => res.destroy()); stream.pipe(res);
    });
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => resolve(server));
  });
}
module.exports = { startServer, parseRpcOrigins };
