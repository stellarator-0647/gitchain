# 开发与协作

## 环境

Git 2.40+、Go 1.27+、Foundry（forge / anvil）、Node.js 22.18+。Windows 上若装有 MinGW gcc，Go 程序请用 `CGO_ENABLED=0` 构建（devnet 脚本已默认）。

## 常用命令

```bash
infra/scripts/devnet.sh up              # 本地链 + 合约 + 三个节点
infra/scripts/devnet.sh restart-nodes   # 只重编译、重启节点，保留链和数据
infra/scripts/demo.sh                   # 核心闭环演示
npm --prefix web install && npm --prefix web run dev   # 前端 http://localhost:5173
```

## 测试

```bash
go vet ./... && go test ./...           # 含三节点端到端，Windows 上约 4–6 分钟
cd contracts && forge test
npm --prefix verifier test
npm --prefix web test && npm --prefix web run build
npm --prefix desktop test
```

Windows 下也可以运行 `infra/scripts/verify.ps1` 一次跑完以上全部检查。

## 约定

- 协议、签名或证明包格式的改动，必须同时更新 Go 实现、`verifier/` 和 `testdata/vectors.json`（`GITCHAIN_WRITE_VECTORS=1 go test ./cmd/vectors` 重新生成），并在 `docs/protocol.md` 记录。
- 合约改动后运行 `infra/scripts/export-abi.sh` 同步 Go 端 ABI。
- 不提交私钥、节点令牌、`.devnet/`、`deployments/<chainId>.json`、构建产物（`bin/`、`web/dist/`、`desktop/ui/`、`desktop/release/`）。
- 一个 PR 只做一件事；提交前跑相关测试，在 PR 描述里写清验证范围。
