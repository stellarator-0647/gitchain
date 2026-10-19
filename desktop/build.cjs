const { execFileSync } = require('node:child_process');
const fs = require('node:fs');
const path = require('node:path');
async function build() {
  const root = path.resolve(__dirname, '..');
  // Run npm via cmd on Windows (npm.cmd cannot be execFile'd directly).
  if (process.platform === 'win32') execFileSync('cmd.exe', ['/d','/s','/c','npm run build'], {cwd:path.join(root,'web'),stdio:'inherit'});
  else execFileSync('npm',['run','build'],{cwd:path.join(root,'web'),stdio:'inherit'});
  fs.cpSync(path.join(root,'web','dist'),path.join(__dirname,'ui'),{recursive:true});
  const { packager } = await import('@electron/packager');
  const outputs = await packager({dir:__dirname,out:path.join(__dirname,'release'),name:'GitChain',platform:'win32',arch:'x64',overwrite:true,asar:true,prune:true,
    ignore:[/^\/release(?:\/|$)/,/^\/screenshots(?:\/|$)/,/^\/desktop(?:\/|$)/,/^\/.*\.test\.cjs$/],
    appVersion:'0.2.1',appCopyright:'GitChain course project; Apache-2.0 upstream',win32metadata:{CompanyName:'GitChain',FileDescription:'GitChain desktop client',ProductName:'GitChain'}});
  console.log('Desktop output:',outputs.join(', '));
}
build().catch(error=>{console.error(error);process.exitCode=1;});
