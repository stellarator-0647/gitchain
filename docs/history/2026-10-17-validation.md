# 验证记录（2026-10-17）

## 已确认

- `go vet ./...`：通过（全量命令先执行 vet 后执行 test）。
- Foundry `forge test --root contracts`：25 passed, 0 failed。
- `npm --prefix verifier test`：15 passed, 0 failed。
- `npm --prefix web test`：3 passed, 0 failed。覆盖规范授权、typedData 替换、超长有效期、越权种类、合约替换。
- `npm --prefix desktop test`：1 passed, 0 failed。内部多项断言覆盖 HTTP 方法、Host 头、目录越界、缺失路径和 CSP。
- `npm --prefix desktop run build`：TypeScript 与 Vite 生产构建通过，Windows x64 打包成功；主 bundle 超过 500 kB 的体积警告仍存在。
- 已打包 `GitChain.exe --smoke-test`：实际 Windows 进程退出码 0。title=GitChain，bridge=true，窗口显示中文节点连接对话框与桌面钱包说明。人工查看 desktop/screenshots/desktop-smoke.png，未见文字截断或空白页。
- 当前 Go 程序已以 CGO_ENABLED=0 构建到 bin/gitchain.exe；未重启或重置用户现有节点。

## Go 全量回归

go test -count=1 ./... 本次退出码 0：internal/node 259.532s、internal/protocol 1.632s、cmd/vectors 2.557s。三节点端到端测试通过。新增 TestRejectInactiveDelegation 在全量启动后加入，另行定向运行通过（0.809s），覆盖未来及过期授权的安装和取用。

## 发布物

`desktop/release/GitChain-Windows-x64-0.2.1.zip`，163451840 字节。
SHA256：E8886C18309E83D41AE5ECEA3CF3280B1FB4EB596D1EE83E559D1C426C6F5AF5。
完整目录：desktop/release/GitChain-win32-x64。

## 未验证 / 不代表

- 本次没有重新操作 Chrome MetaMask，没有新发起链上交易。
- 桌面截图证明的是客户端加载，不是节点同步或钱包签名验收。
- 没有做安装器、系统代码签名、不同 Windows 机器、杀毒兼容性或公网发布测试。
- 没有宣称已完成全面安全审计。

