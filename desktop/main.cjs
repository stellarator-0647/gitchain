const { app, BrowserWindow, ipcMain, shell, dialog, session } = require('electron');
const path = require('node:path');
const fs = require('node:fs/promises');
const { startServer } = require('./server.cjs');
let server;
const origin = 'http://127.0.0.1:5174';
if (!app.requestSingleInstanceLock()) app.quit();
else {
  app.on('second-instance', () => { const w = BrowserWindow.getAllWindows()[0]; if (w) { if (w.isMinimized()) w.restore(); w.focus(); } });
  app.whenReady().then(async () => {
    server = await startServer(path.join(__dirname, 'ui'));
    session.defaultSession.setPermissionRequestHandler((_wc, _permission, done) => done(false));
    ipcMain.handle('open-wallet-browser', (event) => {
      if (new URL(event.senderFrame.url).origin !== origin) throw Error('Invalid sender');
      return shell.openExternal(origin);
    });
    const win = new BrowserWindow({ width: 1280, height: 860, minWidth: 800, minHeight: 600, title: 'GitChain', autoHideMenuBar: true,
      webPreferences: { preload: path.join(__dirname, 'preload.cjs'), contextIsolation: true, nodeIntegration: false, sandbox: true } });
    win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
    win.webContents.on('will-navigate', (event, url) => { if (new URL(url).origin !== origin) event.preventDefault(); });
    await win.loadURL(origin);
    if (process.argv.includes('--smoke-test')) {
      await new Promise(resolve => setTimeout(resolve, 1500));
      const result = await win.webContents.executeJavaScript('({title:document.title, text:document.body.innerText, bridge:!!window.gitchainDesktop})');
      const shot = await win.webContents.capturePage();
      const output = process.env.GITCHAIN_SMOKE_DIR || app.getPath('temp');
      await fs.mkdir(output, {recursive:true});
      await fs.writeFile(path.join(output,'desktop-smoke.json'), JSON.stringify(result,null,2));
      await fs.writeFile(path.join(output,'desktop-smoke.png'),shot.toPNG());
      app.quit();
    }
  }).catch(error => { dialog.showErrorBox('GitChain 启动失败', error.message + '\n请检查端口 5174 是否被占用。'); app.quit(); });
}
app.on('window-all-closed', () => app.quit());
app.on('before-quit', () => { if (server) server.close(); });
