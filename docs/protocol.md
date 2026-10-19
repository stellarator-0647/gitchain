# GitChain 协议规范 v2

本文是代码实现的规范版本，对应《策划方案 v2.1》第 4–8 节。字节级结果以 `testdata/vectors.json` 为准；修改本文须三人同意，并同步更新 Go、TypeScript 和向量。

## 1. EIP-712

```
Domain = { name: "GitChain", version: "2", chainId, verifyingContract: RoomRegistry }
Event(bytes32 room,address author,uint64 seq,bytes32 authorPrev,bytes32 seen,uint64 ts,uint8 kind,bytes32 target,bytes32 data1,bytes32 data2)
eventId = keccak256(0x1901 || domainSeparator || hashStruct(Event))   // 即钱包签名的摘要
```

- chainId 与合约地址只来自可信部署清单（`deployments/<chainId>.json`），不读 Git 文件或证明包。
- 签名：65 字节 r‖s‖v，v ∈ {27, 28}，s ≤ n/2。高 s 签名直接拒绝，不做翻转；节点存储时把 v 规范为 27/28。
- `seen`：发送时本地 main 的 HEAD，40 位 SHA-1 左对齐、右补零到 32 字节。是作者签署的因果声明，不证明“未见过”。
- `ts`：作者自报的 Unix 秒，只作参考和排序键，不作为可信时间。

## 2. kind

| kind | 含义 | target | data1 | data2 | 层级 |
| --- | --- | --- | --- | --- | --- |
| 1 | 普通消息 | 0 | keccak256(salt ‖ utf8(body)) | 0 | 必做 |
| 2 | 主动签名回执 | 原消息 eventId | 原消息 data1 | 0 | 加分 |
| 3 | 删除墓碑 | 被删 eventId | uint256(proposalId) | 0 | 必做 |
| 4 | 合同交易引用 | agreementId | 交易哈希 | docHash | 加分 |
| 6 | 合同修订 | agreementId | docHash | 分支 commit | 加分 |

通用规则：author ≠ 0；seq = 0 时 authorPrev = 0，seq > 0 时 authorPrev ≠ 0。

## 3. 存储

```
main     meta/room.json                              位置提示，不可信
         events/<author 小写>/<seq>/<eventId 小写>.json  SignedEvent {event, sig}
content  manifest.json                               {generation, deleted[]}
         blobs/<eventId 小写>.json                    {salt, body}
```

- 每个钱包在每个房间只有一个写入节点；节点在锁内分配 seq 和 authorPrev。
- 提交说明只含 eventId，不含正文。content 分支用 plumbing 命令写入，从不检出。
- body 是 1–16384 字节的合法 UTF-8；salt 为 32 字节随机数。

## 4. Merkle 树

- 叶子 = keccak256(0x00 ‖ eventId ‖ keccak256(sig))，按 (author 地址字节, seq, eventId) 升序排列。
- 内部节点 = keccak256(min(a,b) ‖ max(a,b))；奇数层最后一个节点原样提升；单叶树的根就是该叶；禁止空树。
- 根覆盖该提交时刻 main 中的全部事件；count = 叶子数。

## 5. 检查点与合法历史

- 只有 RoomRegistry 中该房间的 anchorWriter 能调用 `AnchorLog.anchor`。第一个检查点 prevIndex = 2²⁵⁶−1，之后 prevIndex = 上一编号，count 严格递增，同一 commit 不能锚定两次。
- 锚定节点只锚定“快照中全部事件有效、且已推送到远端”的 HEAD。
- 客户端对每个已确认检查点检查：编号链正确、提交者为 anchorWriter、commit 本地存在且是 HEAD 的祖先、继承前一检查点、在该 commit 重算的 root/count 与链上一致。失败时房间状态为 `checkpoint_missing` 或 `checkpoint_invalid`，不静默忽略。
- 事件被第一个覆盖它的检查点标为 `anchored`，存在时间上界为该检查点所在区块。

## 6. 事件判定

按顺序检查，任一失败则 evidence = invalid 并给出原因：

1. JSON 可解析；签名规范；签名恢复出 author。
2. 文件路径等于 `events/<author>/<seq>/<eventId>.json`。
3. 形状规则（第 2 节）、room 一致、author 是固定成员。
4. 作者链：seq 从 0 连续，authorPrev 链接上一条；出现同 seq 不同事件（双重发送）或缺口时，该作者从此处起的事件全部隔离。
5. 墓碑须引用一个链上已执行、且目标相同的删除提案。

三个独立维度：

| 维度 | 取值 |
| --- | --- |
| evidence | pending · anchored · invalid |
| content | available · missing（缺失或与承诺不符）· purged（删除已授权）|
| redaction | none · authorized（链上已执行）· applied（另有有效墓碑）|

删除一经授权，诚实节点立即隐藏正文并从自己的 content 快照移除（重建并递增 generation），旧正文回传也不会复活。

## 7. 同步与合并

1. `git fetch` main 与 content。
2. 对本地 main ∪ origin/main 的事件树做第 6 节 1–4 条检查；有任何无效事件则**拒绝合并**，写入 `state/quarantine/latest.json` 并报警。
3. main：可快进则快进，否则 `merge --no-ff`。事件路径含 eventId，正常情况下不会冲突；冲突即中止并报告。
4. content：分叉时不做文本合并，按“两边正文并集 − 两边删除集合并集”重建，父提交为两边，generation 取较大值 + 1。
5. push main 与 content；被拒则下一轮重来。离线分区恢复后走同一流程。

## 8. 显示顺序

依赖：authorPrev 指向的事件；以及 seen 提交中已存在的全部事件。拓扑排序，入度为零的事件中按 (ts, author, seq, eventId) 取最小者。所有节点拿到相同事件集合后得到相同顺序。

## 9. 证明包（version 2）

```
{ version, chainId, room, contracts{registry, anchorLog, governance},
  event{event, sig}, eventId,
  content {salt, body} | null,
  inclusion {checkpointIndex, root, count, leafIndex, proof[]},
  redaction {proposalId, tombstone{event, sig}, tombstoneInclusion} | null }
```

验证器（`verifier/`）步骤：部署一致 → 签名与 eventId → 房间与成员 → 检查点（root/count/提交者/区块）→ Merkle 路径 → 正文承诺，或删除提案已执行 + 墓碑签名、成员与锚定。链读取失败记为 unknown，不记为通过。

## 10. 本地 API 安全

只监听回环地址；所有 `/api` 与 `/ws` 需要节点令牌（`<data>/state/api-token`）；带 Origin 的请求须在白名单内；请求体 ≤ 64 KiB 且必须是合法 UTF-8（否则 JSON 解码会把坏字节悄悄替换成 U+FFFD，用户会签下自己没写过的文字）。

## 11. 链下短期授权（加分项）

```
Delegation(bytes32 room,address wallet,address delegate,uint64 validFrom,uint64 validUntil,uint256 kinds)
delegationId = keccak256(0x1901 || domainSeparator || hashStruct(Delegation))   // 钱包签名的摘要
SignedEvent  = { event, sig, delegation?: { delegation, walletSig } }          // sig 由 delegate 签
```

- 钱包签一次 Delegation，之后浏览器里的临时密钥（delegate）签事件；事件的 author 仍是钱包。临时密钥只存在当前标签页的 sessionStorage。
- 静态规则：wallet ≠ delegate；`0 < validUntil − validFrom ≤ 7200` 秒；kinds 只能是消息（bit 1）和回执（bit 2）的子集，墓碑与合同事件必须钱包本人签名；delegation.wallet = event.author，delegation.room = event.room；walletSig 与 sig 都须为规范签名。
- 叶子：`keccak256(0x01 ‖ eventId ‖ keccak256(sig) ‖ delegationId)`，把授权也承诺进检查点，防止事后换成同一密钥的更长授权。非委托事件的叶子不变。
- 时间有效性**只看区块时间**：委托事件须被区块时间 ≤ validUntil 的检查点首次覆盖才有效。尚未锚定且本地时间已过 validUntil 的、或锚定时已过期的，evidence = invalid 并标 `delegationExpired`。
- 过期判定在锚定之后进行：检查点快照照常包含这些事件（结构有效），它们不会阻止后续锚定。
- 不做吊销：节点 `DELETE /api/delegations` 只是不再接受新的临时密钥签名；已签的事件按自身授权判定。离线分区中接近到期时发出的消息，可能因联网后才锚定而失效，这是已知限制。
- 节点接口：`POST /api/delegations/draft {delegate, seconds}` → typedData；`POST /api/delegations {delegation, walletSig}`；`DELETE /api/delegations`；`/api/status` 返回当前授权。提交时 `/api/events/commit` 自动识别签名者是钱包还是临时密钥。

## 2026-10-17 授权与客户端补充

委托事件首次覆盖的检查点时间必须位于闭区间 [validFrom, validUntil]；不再仅检查截止时间。节点本地安装与使用还要求当前时间位于 [validFrom, validUntil)。时间无效仍不阻塞锚定。浏览器签授权前比较 typedData 与授权 JSON 摘要，并核对部署与请求权限。桌面端的能力/钱包边界见 CLIENT.md，完整修改见 CHANGES-2026-10-17.md。
