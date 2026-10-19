const { contextBridge, ipcRenderer } = require('electron');
contextBridge.exposeInMainWorld('gitchainDesktop', Object.freeze({ openWalletBrowser: () => ipcRenderer.invoke('open-wallet-browser') }));
