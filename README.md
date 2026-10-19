# GitChain

基于 Git 与区块链的去中心化可信群聊（区块链课程项目）。

在 [Xelckis/GitChat](https://github.com/Xelckis/GitChat)（Apache-2.0，基线提交 `eb9c691`）上改造：保留 Go 节点、本地 HTTP/WebSocket 和周期同步的思路，重写消息模型、身份、同步与验证，新增链上合约、独立验证器、浏览器前端和 Windows 桌面客户端。协议细则见 [docs/protocol.md](docs/protocol.md)。

## 它解决什么

三方（如甲方、乙方、监理）在群里沟通，需要事后能举证、能发现服务器作恶，又要能按多数同意删除内容：

| 需求 | 做法 |
| --- | --- |
| 证明“这条消息是某个钱包发的，且不晚于某时存在” | EIP-712 钱包签名 + 事件集合的 Merkle 根批量锚定到链上 |
| 发现共享 Git 服务器篡改、扣留或分视图 | 客户端用签名、作者序号链和链上检查点独立核验，不信任远端 |
| 按多数同意删除，并且删除本身可审计 | 链上投票授权 → 墓碑事件 → 各节点清理正文，承诺保留 |
| 断网时各自记录，联网后自动合并 | 签名与 Git commit 解耦，Git 分叉后合并不影响签名 |
| 把单条消息交给第三方核验 | 导出证明包，独立验证器只读链，不需要节点或 Git |
| 不想每条消息都弹钱包 | 钱包签一次短期授权（≤ 2 小时），浏览器临时密钥代签消息 |

## 组成

```
浏览器 / 桌面客户端 ──REST+WS──> 本地 Go 节点 ──git──> 共享 Git 远端（不可信）
                                     │
                                     └──RPC──> RoomRegistry · AnchorLog · Governance（以太坊）
独立验证器：证明包 + 可信部署清单 + RPC
```

| 模块 | 目录 | 状态 |
| --- | --- | --- |
| 协议：EIP-712 事件、签名规范化、内容承诺、Merkle 树、短期授权 | `internal/protocol` | 完成；单测 + 与 go-ethereum 通用 EIP-712 交叉校验 |
| Git 存储：main 事件日志、content 正文分支、同步合并 | `internal/gitstore` | 完成 |
| 验证器：签名/成员/序号链/双重发送/正文/检查点/删除授权/授权时间，确定性排序，合并前隔离 | `internal/verify` | 完成 |
| 链接口：内存实现与 JSON-RPC 实现 | `internal/chain` | 完成 |
| 节点编排与本地 API | `internal/node`、`internal/api`、`cmd/gitchain` | 完成；三节点端到端测试 |
| 合约 | `contracts/` | 完成；25 个 Foundry 测试 |
| 独立验证器与跨语言向量 | `verifier/`、`testdata/vectors.json` | 完成；15 个向量测试 |
| 浏览器前端 | `web/` | 完成；测试签名全流程，MetaMask 签名、治理交易与短期授权已在 Chrome 实测 |
| Windows 桌面客户端（Electron） | `desktop/` | 完成；打包与冒烟测试通过，钱包签名需在系统浏览器中进行 |
| 加分项②–⑥：主动回执、合同协商分支、更换锚定者、增量验证、Sepolia | — | 未开始 |

## 快速开始

需要 Git、Go 1.27+、Foundry、Node.js 22.18+，详见 [CONTRIBUTING.md](CONTRIBUTING.md)。

```bash
infra/scripts/devnet.sh up      # Anvil + 部署合约 + 建房 + bare 远端 + 三个节点（8081/8082/8083）
infra/scripts/demo.sh           # 发送 → 同步 → 锚定 → 导出证明 → 2/3 投票删除 → 墓碑 → 删除后证明
cd verifier && npm ci && node src/cli.ts ../.devnet/demo/<proof>.json ../deployments/31337.json
infra/scripts/devnet.sh down    # 停止；reset 会清空 .devnet/
```

前端：

```bash
cd web && npm ci && npm run dev   # http://localhost:5173
```

在“节点设置”里填节点地址（如 `http://127.0.0.1:8082`）和访问令牌（devnet 为 `devtoken-a/b/c`，正式运行见 `<data>/state/api-token`）。签名方式：

- **临时密钥（默认）**：首次发送时钱包签一次授权，之后由本标签页里的临时密钥签消息；墓碑仍需钱包签名。
- **浏览器钱包**：每条消息由 MetaMask 签名；页面会请求切换（或添加）到节点所在链。
- **测试签名（仅开发）**：节点以 `-dev-key-env` 启动时可用，由节点用公开测试私钥签名。

脚本和测试中的私钥均为 Anvil 公开的测试账户，只能用于本地链。

## Windows 桌面客户端

```powershell
npm --prefix verifier ci; npm --prefix web ci; npm --prefix desktop ci
npm --prefix desktop run build      # 产物：desktop/release/GitChain-win32-x64/GitChain.exe
```

客户端自带界面（固定监听 `127.0.0.1:5174`），连接已运行的节点；Electron 不继承浏览器里的 MetaMask，需要钱包时点“在浏览器中使用钱包”。详见 [docs/client.md](docs/client.md)。

## 测试

```bash
go vet ./... && go test ./...      # 协议单测 + 三节点端到端（内存链）
cd contracts && forge test
npm --prefix verifier test
npm --prefix web test && npm --prefix web run build
npm --prefix desktop test
```

Windows 下 `infra/scripts/verify.ps1` 一次跑完全部检查；GitHub Actions 在每次推送时运行同样的测试。

端到端测试覆盖：三节点排序一致；锚定；证明导出与校验；非锚定者无法锚定；2/3 授权删除（隐藏正文、墓碑、删除后证明）；离线分区合并；伪造事件被拒绝合并并隔离；短期授权有效、不能签墓碑、过期事件不阻塞后续锚定。

## 节点 API

均需请求头 `X-GitChain-Token`；节点只监听回环地址并校验 Origin。

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/api/status` | 房间、钱包、健康度、同步、隔离、检查点、当前授权 |
| GET | `/api/history?page=N` | 每页 50 条，三维状态 evidence / content / redaction |
| POST | `/api/messages/draft` | `{body}` → `{draftId, eventId, typedData}` |
| POST | `/api/events/tombstone-draft` | `{proposalId}`，提案须已在链上执行 |
| POST | `/api/events/commit` | `{draftId, sig}`；sig 来自钱包或已授权的临时密钥 |
| DELETE | `/api/events/draft/:id` | 释放草稿 |
| GET | `/api/proof/:eventId?content=1` | 证明包；删除的消息不含正文 |
| GET | `/api/proposals` | 删除提案及链上状态 |
| POST | `/api/delegations/draft` | `{delegate, seconds}` → 待钱包签名的授权 |
| POST / DELETE | `/api/delegations` | 安装 / 停用临时密钥授权 |
| POST | `/api/anchor/now` | 仅锚定节点（演示用） |
| GET | `/api/alerts`、`/api/sync/status`、`/api/chain/config` | |
| WS | `/ws?token=` | 推送 message / state / sync / anchor / redaction |

提案、投票、执行由前端用钱包直接发交易；脚本可用 `bin/gitchain-dev propose|vote|execute`。

## 目录

```
cmd/gitchain        节点主程序            cmd/gitchain-dev   链上辅助命令（测试密钥）
cmd/vectors         测试向量生成          internal/          协议、存储、验证、链、节点、API
contracts/          Foundry 合约          verifier/          独立验证器（TypeScript）
web/                浏览器前端            desktop/           Windows 桌面客户端（Electron）
infra/scripts/      devnet / demo / verify   deployments/    可信部署清单（本地生成，不入库）
docs/               协议、客户端、历史记录   testdata/       跨语言向量
```

## 已知限制

- 指定锚定节点是信任假设：它离线则不产生新检查点，作恶只能被发现、不能自动恢复。
- 删除只清理各节点管理的当前正文快照；Git 历史中的旧对象需在维护窗口清理，别人已保存的副本无法删除。
- 正文对成员和 Git 服务可见，没有端到端加密。
- 短期授权没有链上吊销；时间有效性以检查点区块时间判断，节点时钟与链时间偏差较大时需留意。
- 桌面端未做代码签名，Windows 可能提示未知发布者。

## 致谢与许可

Apache-2.0，见 [LICENSE](LICENSE) 与 [NOTICE](NOTICE)。基于 Xelckis/GitChat；合约测试使用 forge-std。
