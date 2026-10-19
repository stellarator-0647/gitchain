# Windows 客户端

## 使用

发布目录：`desktop/release/GitChain-win32-x64/GitChain.exe`。整个目录需要一起保留，不能只复制 exe；解压便携包后双击即可。无需运行 Vite，也无需安装 Node.js 来使用已打包客户端。

客户端自带界面，不自带运行中的区块链或房间节点。它连接你已部署的节点（例如 http://127.0.0.1:8082），访问令牌在对应 data/state/api-token 中，或由开发脚本设置。客户端不自动启动/重置 Anvil，不包含测试私钥，关闭窗口也不会关闭现有节点。

客户端独占本机 5174 端口，默认只监听 127.0.0.1；占用时显示错误，不复用未知服务。节点需允许 Origin http://127.0.0.1:5174。本次已修改 Go 程序默认白名单，旧进程需重新编译并 restart-nodes。

## 钱包

Electron 不会继承 Chrome 的 MetaMask。客户端顶部“在浏览器中使用钱包”打开相同的本机界面。浏览器里重新填写节点地址/令牌，选择测试账户和 Anvil 网络，签名行为仍由用户确认。此入口使用系统默认浏览器；若默认浏览器不是装有 MetaMask 的 Chrome，手动复制 http://127.0.0.1:5174 到 Chrome。

桌面和 Chrome 存储相互独立，不传递令牌/私钥。临时授权也各自独立；桌面模式无法自动复用 Chrome 的临时密钥。节点启用 dev signer 时可在桌面进行明确标识的本地演示。正式钱包功能在浏览器中完成。

## 构建

```powershell
cd .\verifier
npm ci
cd ..\web
npm ci
cd ..\desktop
npm ci
npm test
npm run build
```

`npm run build` 先构建 web，再复制到 desktop/ui，最后打包 Electron。用 npm ci 可复现 lockfile。release/ui/node_modules 是生成目录，不入库。客户端是未做系统代码签名的课程原型，不是安装程序；Windows 可能显示未知发布者。

## 边界

渲染进程 sandbox/contextIsolation 开启、nodeIntegration 关闭；拒绝新窗口、跨源主窗口跳转、权限请求。预加载仅暴露打开固定本机界面的能力；不暴露磁盘、命令执行或任意 URL。静态服务校验 Host、路径和 HTTP 方法并设置 CSP。RPC 仅允许本机端点；远程部署若要支持，须显式调整 CSP 并审查可信配置。

## 冒烟

```powershell
$env:GITCHAIN_SMOKE_DIR='.\desktop\screenshots'
& .\release\GitChain-win32-x64\GitChain.exe --smoke-test
```

输出 desktop-smoke.json 与 png，证明已打包窗口真实加载。它不证明链或钱包已连接。验证记录见 docs/history/2026-10-17-validation.md。
