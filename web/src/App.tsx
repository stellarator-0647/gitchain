import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Hex } from "viem";
import { NodeClient, NodeError, type NodeSettings } from "./api";
import { governance, hasWallet, signDraft } from "./wallet";
import type { Draft, GitView, Message, OrderProposal, Proposal, Status } from "./types";
import { contentLabel, evidenceLabel, explainError, healthLabel, redactionLabel, short, time } from "./labels";
import { VerifyPanel } from "./VerifyPanel";
import { mergePages } from "./history";
import { proposalState } from "./proposal-state";
import { createDelegation, currentDelegate, forgetDelegate, signWithDelegate } from "./delegate";

type Mode = "wallet" | "delegate" | "dev";
type Tab = "proposals" | "room" | "git" | "verify";

const SETTINGS_KEY = "gitchain.node";

function loadSettings(): NodeSettings {
  try {
    const raw = localStorage.getItem(SETTINGS_KEY);
    if (raw) return JSON.parse(raw);
  } catch {
    /* storage unavailable */
  }
  return { url: "http://127.0.0.1:8081", token: "" };
}

function saveSettings(s: NodeSettings) {
  try {
    localStorage.setItem(SETTINGS_KEY, JSON.stringify(s));
  } catch {
    /* storage unavailable */
  }
}

function download(name: string, data: unknown) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(data, null, 2)], { type: "application/json" }));
  const a = Object.assign(document.createElement("a"), { href: url, download: name });
  a.click();
  URL.revokeObjectURL(url);
}

export default function App() {
  const [settings, setSettings] = useState<NodeSettings>(loadSettings);
  const [showSettings, setShowSettings] = useState(() => !loadSettings().token);
  const client = useMemo(() => new NodeClient(settings), [settings]);
  const [status, setStatus] = useState<Status | null>(null);
  const [messages, setMessages] = useState<Message[]>([]);
  const [proposals, setProposals] = useState<Proposal[]>([]);
  // Proposals come from the chain via the node; a failed read keeps the last list and says so.
  const [proposalsState, setProposalsState] = useState<"loading" | "ok" | "error">("loading");
  const [orders, setOrders] = useState<OrderProposal[]>([]);
  // History pages from the newest end (page 1 = latest 50); every refresh reloads all loaded pages.
  const [pages, setPages] = useState(1);
  const [total, setTotal] = useState(0);
  const [error, setError] = useState(""); // last failed action; stays until the next action
  const [connError, setConnError] = useState(""); // background refresh failures
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");
  const [chosenMode, setMode] = useState<Mode>(hasWallet() ? "delegate" : "dev");
  // Fall back to the wallet when the node has no TEST signer.
  const mode: Mode = chosenMode === "dev" && status && !status.devSigner && hasWallet() ? "wallet" : chosenMode;
  const [tab, setTab] = useState<Tab>("proposals");
  const listEnd = useRef<HTMLDivElement>(null);

  const refresh = useCallback(async () => {
    if (!settings.token) return;
    try {
      const pageNos = Array.from({ length: pages }, (_, i) => pages - i); // oldest page first
      const [st, hs, ps, os] = await Promise.all([
        client.status(),
        Promise.all(pageNos.map((n) => client.history(n))),
        client.proposals().then((v) => v ?? [], () => null),
        client.orders().then((v) => v ?? [], () => null),
      ]);
      setStatus({ ...st, alerts: st.alerts ?? [] });
      setMessages(mergePages(hs.map((h) => h.messages ?? [])));
      setTotal(hs[hs.length - 1]?.total ?? 0);
      if (ps) setProposals(ps);
      if (os) setOrders(os);
      setProposalsState(ps ? "ok" : "error");
      setConnError("");
    } catch (e) {
      setConnError((e as Error).message);
    }
  }, [client, settings.token, pages]);

  useEffect(() => {
    void refresh();
    if (!settings.token) return;
    const stop = client.subscribe(() => void refresh());
    const t = window.setInterval(() => void refresh(), 5000);
    return () => {
      stop();
      window.clearInterval(t);
    };
  }, [client, refresh, settings.token]);


  // Follow new messages only; loading older pages must not jump to the bottom.
  const lastId = messages[messages.length - 1]?.eventId;
  useEffect(() => {
    // Recent Chromium returns a Promise from scrollIntoView; never return it to React.
    void listEnd.current?.scrollIntoView({ block: "end" });
  }, [lastId]);

  /** Runs a user action; returns whether it succeeded. Errors stay visible until the next action. */
  const run = async (label: string, fn: () => Promise<string | void>): Promise<boolean> => {
    setBusy(label);
    setError("");
    setNotice("");
    try {
      const msg = await fn();
      if (msg) setNotice(msg);
      await refresh();
      return true;
    } catch (e) {
      const err = e as NodeError & { shortMessage?: string };
      console.error(`[gitchain] ${label} failed`, e);
      setError(`${label}失败：${explainError(err.shortMessage ?? err.message)}`);
      return false;
    } finally {
      setBusy("");
    }
  };

  /** Signs a node-prepared draft with the delegate key, the wallet or the TEST signer, and commits it. */
  const signAndCommit = async (d: Draft): Promise<Hex> => {
    try {
      if (mode === "dev") return (await client.devSign(d.draftId)).eventId;
      const st = status!;
      const delegable = ["1", "2"].includes(d.typedData.message.kind);
      if (mode === "delegate" && delegable) {
        let del = currentDelegate(st);
        if (!del) {
          setBusy("请在钱包中授权临时密钥（1 小时内免弹窗）");
          del = await createDelegation(client, (await client.status()));
        }
        return (await client.commit(d.draftId, await signWithDelegate(d, st, del))).eventId;
      }
      const sig = await signDraft(d.typedData, st.wallet, st.chainId, st.rpc);
      return (await client.commit(d.draftId, sig)).eventId;
    } catch (e) {
      await client.cancelDraft(d.draftId).catch(() => undefined);
      throw e;
    }
  };

  const gov = async (fn: "proposeRedaction" | "vote" | "execute" | "proposeOrder" | "voteOrder",
    args: { target?: Hex; other?: Hex; reason?: string; id?: number; support?: boolean }) => {
    if (mode === "dev") {
      if (fn === "proposeRedaction") return client.devPropose(args.target!, args.reason ?? "");
      if (fn === "vote") return client.devVote(args.id!, args.support!);
      if (fn === "proposeOrder") return client.devProposeOrder(args.target!, args.other!);
      if (fn === "voteOrder") return client.devVoteOrder(args.id!, args.support!);
      return client.devExecute(args.id!);
    }
    const st = status!;
    const { keccak256, toHex } = await import("viem");
    if (fn === "proposeRedaction") return governance(st, fn, [st.room, args.target!, keccak256(toHex(args.reason ?? ""))]);
    if (fn === "vote" || fn === "voteOrder") return governance(st, fn, [BigInt(args.id!), args.support!]);
    if (fn === "proposeOrder") return governance(st, fn, [st.room, args.target!, args.other!]);
    return governance(st, fn, [BigInt(args.id!)]);
  };

  const byId = useMemo(() => new Map(messages.map((m) => [m.eventId.toLowerCase(), m])), [messages]);

  return (
    <div className="app">
      <header className="top">
        <div className="brand">GitChain</div>
        {status ? (
          <div className="facts">
            <span title={status.room}>房间 {short(status.room)}</span>
            <span title={status.wallet}>成员 {short(status.wallet)}</span>
            <span className={`pill health-${status.health}`}>{healthLabel[status.health]}</span>
            <span className={`pill ${status.syncPaused ? "warn" : status.sync.error && !status.sync.error.startsWith("push") ? "bad" : status.sync.error ? "warn" : "ok"}`} title={status.sync.error ?? ""}>
              {status.syncPaused ? "离线（同步已暂停）" : status.sync.error
                ? status.sync.error.startsWith("push") ? "推送重试中" : "同步异常"
                : status.sync.unpushed ? `待推送 ${status.sync.unpushed}` : "已同步"}
            </span>
            {status.latestCheckpoint ? (
              <span title={status.latestCheckpoint.root}>检查点 #{status.latestCheckpoint.index} · 区块 {status.latestCheckpoint.blockNo}</span>
            ) : (
              <span>尚无检查点</span>
            )}
          </div>
        ) : (
          <div className="facts muted">未连接节点</div>
        )}
        <div className="right">
          <label className="mode">
            签名方式
            <select value={mode} onChange={(e) => setMode(e.target.value as Mode)}>
              <option value="delegate" disabled={!hasWallet()}>临时密钥（钱包授权一次）</option>
              <option value="wallet" disabled={!hasWallet()}>浏览器钱包（每条签名）</option>
              <option value="dev" disabled={!status?.devSigner}>测试签名（仅开发）</option>
            </select>
          </label>
          <button className="ghost" onClick={() => setShowSettings(true)}>节点设置</button>
        </div>
      </header>

      {window.gitchainDesktop && <div className="banner info">Windows 客户端已启动。MetaMask 操作请在浏览器中完成；节点地址和访问令牌需要单独填写。<button className="link" onClick={() => void window.gitchainDesktop!.openWalletBrowser()}>在浏览器中使用钱包</button></div>}
      {connError && <div className="banner error" role="alert">{connError}</div>}
      {(error || notice || busy) && (
        <div className={`banner ${error ? "error" : "info"}`} role="status">
          {busy ? `${busy}…` : error || notice}
          {error && <button className="link" onClick={() => setError("")}>关闭</button>}
        </div>
      )}
      {mode === "delegate" && status && (
        <div className="banner dev">
          {status.delegation && currentDelegate(status)
            ? `临时密钥 ${short(status.delegation.delegate)} 已授权，有效至 ${time(status.delegation.validUntil)}；删除墓碑仍需钱包签名。`
            : "临时密钥模式：首次发送时请钱包授权一次，之后 1 小时内发消息不再弹窗。"}
          {status.delegation && (
            <button className="link" onClick={() => void run("停用本地临时密钥", async () => {
              forgetDelegate(status);
              await client.clearDelegation();
              return "已停止使用临时密钥（已签的消息不受影响）";
            })}>停止使用</button>
          )}
        </div>
      )}
      {status?.syncPaused && (
        <div className="banner offline">
          同步已暂停（模拟断网）：新消息只提交到本节点的 Git main 分支，不拉取也不推送；恢复后自动合并。
          <button className="link" onClick={() => void run("恢复同步", async () => { await client.pauseSync(false); return "已恢复同步，几秒内与其他节点合并"; })}>恢复同步</button>
        </div>
      )}
      {mode === "dev" && status?.devSigner && (
        <div className="banner dev">测试签名模式：消息和投票由节点用测试私钥签名，只用于本地演示。</div>
      )}

      <main className="layout">
        <section className="chat">
          <div className="messages">
            {messages.length === 0 && <p className="muted empty">还没有消息。</p>}
            {messages.length > 0 && messages.length < total && (
              <button className="ghost older" onClick={() => setPages((n) => n + 1)}>加载更早消息（已显示 {messages.length} / {total}）</button>
            )}
            {messages.map((m) => (
              <MessageItem
                key={m.eventId}
                m={m}
                mine={!!status && m.author.toLowerCase() === status.wallet.toLowerCase()}
                target={m.target ? byId.get(m.target.toLowerCase()) : undefined}
                byId={byId}
                onOrder={(peer) =>
                  run("发起排序投票", async () => {
                    await gov("proposeOrder", { target: m.eventId, other: peer });
                    setTab("proposals");
                    return "已在链上发起排序投票，请各成员在“删除提案”页下方投票";
                  })
                }
                onProof={(withContent) =>
                  run("导出证明", async () => {
                    download(`gitchain-proof-${m.eventId.slice(2, 10)}.json`, await client.proof(m.eventId, withContent));
                    return "证明包已下载";
                  })
                }
                onRedact={(reason) =>
                  run("发起删除提案", async () => {
                    await gov("proposeRedaction", { target: m.eventId, reason });
                    setTab("proposals");
                    return "已发起删除提案，等待成员投票";
                  })
                }
              />
            ))}
            <div ref={listEnd} />
          </div>
          <Composer
            disabled={!status || !!busy}
            onSend={(body) =>
              run("发送", async () => {
                const d = await client.messageDraft(body);
                await signAndCommit(d);
                return "已签名并保存，等待同步与锚定";
              })
            }
          />
        </section>

        <aside className="side">
          <nav className="tabs">
            <button className={tab === "proposals" ? "on" : ""} onClick={() => setTab("proposals")}>删除提案</button>
            <button className={tab === "room" ? "on" : ""} onClick={() => setTab("room")}>
              房间状态{status && status.alerts.length > 0 ? ` (${status.alerts.length})` : ""}
            </button>
            <button className={tab === "git" ? "on" : ""} onClick={() => setTab("git")}>Git 仓库</button>
            <button className={tab === "verify" ? "on" : ""} onClick={() => setTab("verify")}>举证验证</button>
          </nav>
          {tab === "proposals" && (
            <Proposals
              proposals={proposals}
              members={status?.members || 3}
              state={proposalsState}
              onRetry={() => void refresh()}
              olderAvailable={messages.length < total}
              onLoadOlder={() => setPages((n) => n + 1)}
              byId={byId}
              me={status?.wallet}
              busy={!!busy}
              onVote={(id, support) => run("投票", async () => void (await gov("vote", { id, support })))}
              onExecute={(id) => run("执行提案", async () => void (await gov("execute", { id })))}
              orders={orders}
              onVoteOrder={(id, aFirst) => run("排序投票", async () => void (await gov("voteOrder", { id, support: aFirst })))}
              onTombstone={(id) =>
                run("签署墓碑", async () => {
                  await signAndCommit(await client.tombstoneDraft(id));
                  return "墓碑已签署，等待锚定";
                })
              }
            />
          )}
          {tab === "room" && status && (
            <RoomPanel status={status} onAnchor={() => run("锚定", async () => {
              const r = await client.anchorNow();
              if (r.anchored) return `已提交检查点 #${r.latest?.index ?? "?"}（区块 ${r.latest?.blockNo ?? "?"}）`;
              if (r.skipped === "unpushed") return "本节点还有未推送的提交，等推送到 Git 远端后再锚定（其他成员必须能拉取到被锚定的提交）";
              return `没有新的待锚定消息：当前消息都已在检查点 #${r.latest?.index ?? "?"} 及之前锚定`;
            })} />
          )}
          {tab === "git" && status && (
            <GitPanel client={client} byId={byId} paused={status.syncPaused}
              onPause={(p) => run(p ? "暂停同步" : "恢复同步", async () => {
                await client.pauseSync(p);
                return p ? "已暂停同步：现在发送的消息只在本节点 Git 中，其他成员看不到" : "已恢复同步，几秒内与其他节点合并";
              })} />
          )}
          {tab === "verify" && <VerifyPanel />}
        </aside>
      </main>

      {showSettings && (
        <SettingsDialog
          initial={settings}
          onSave={(s) => {
            saveSettings(s);
            setSettings(s);
            setShowSettings(false);
          }}
          onClose={() => setShowSettings(false)}
        />
      )}
    </div>
  );
}

function MessageItem(props: {
  m: Message; mine: boolean; target?: Message; byId: Map<string, Message>;
  onProof: (withContent: boolean) => void; onRedact: (reason: string) => Promise<boolean>; onOrder: (peer: Hex) => void;
}) {
  const { m, mine, target } = props;
  const [showConc, setShowConc] = useState(false);
  const conc = m.concurrent ?? [];
  const settled = conc.filter((c) => c.order);
  const [asking, setAsking] = useState(false);
  const [reason, setReason] = useState("");
  const isTomb = m.kind === 3;
  return (
    <article className={`msg ${mine ? "mine" : ""} ${m.evidence === "invalid" ? "bad" : ""} ${isTomb ? "system" : ""}`}>
      <div className="meta">
        <span title={m.author}>{short(m.author)}</span>
        <span>#{m.seq}</span>
        <span>{time(m.ts)}</span>
      </div>
      <div className="body">
        {isTomb ? (
          <em>删除墓碑：依提案执行，指向 {short(m.target)}{target ? "" : "（未知事件）"}</em>
        ) : m.kind === 1 && m.body !== undefined ? (
          m.body
        ) : m.redaction !== "none" ? (
          <em>该消息已按投票授权删除</em>
        ) : (
          <em>{m.reason ?? "正文不可用"}</em>
        )}
      </div>
      <div className="badges">
        <span className={`b ev-${m.evidence}`} title={m.anchorBlock ? `区块 ${m.anchorBlock}` : ""}>
          {evidenceLabel[m.evidence]}
          {m.checkpointIndex !== undefined ? ` #${m.checkpointIndex}` : ""}
        </span>
        {m.kind === 1 && <span className={`b ct-${m.content}`}>{contentLabel[m.content]}</span>}
        {m.redaction !== "none" && <span className="b rd">{redactionLabel[m.redaction]}{m.proposalId ? ` · 提案 ${m.proposalId}` : ""}</span>}
        {m.evidence === "invalid" && <span className="b reason">{m.reason}</span>}
        {conc.length > 0 && (
          <button className={`b conc ${settled.length === conc.length ? "done" : ""}`} title="发送时双方都没有看到对方的消息，签名无法证明谁先谁后" onClick={() => setShowConc(!showConc)}>
            {settled.length === conc.length ? `并发 · 顺序已由投票确定` : `与 ${conc.length} 条消息并发`}
          </button>
        )}
        {m.evidence === "anchored" && (
          <span className="acts">
            <button className="link" onClick={() => props.onProof(m.content === "available")}>导出证明</button>
            {m.kind === 1 && m.redaction === "none" && <button className="link" onClick={() => setAsking(!asking)}>申请删除</button>}
          </span>
        )}
      </div>
      {showConc && (
        <div className="conc-list">
          <div className="muted small">以下消息与本条“并发”：签名时双方都没看到对方，系统无法证明先后。显示顺序只是统一的排列规则；需要确定先后时，可发起链上排序投票（过半成员同意即生效）。</div>
          {conc.map((c) => {
            const peer = props.byId.get(c.eventId.toLowerCase());
            const text = peer ? (peer.body ?? (peer.kind === 3 ? "删除墓碑" : "（正文不可用）")) : short(c.eventId);
            return (
              <div key={c.eventId} className="row small">
                <span className="grow">{peer ? short(peer.author) : ""}：{text.length > 24 ? text.slice(0, 24) + "…" : text}</span>
                {c.order ? <span className="b ok">经投票：本条在其{c.order === "before" ? "之前" : "之后"}（排序 {c.orderProposal}）</span>
                  : c.orderProposal ? <span className="b warn">排序投票 {c.orderProposal} 进行中</span>
                  : <button className="link" onClick={() => props.onOrder(c.eventId)}>要求排序</button>}
              </div>
            );
          })}
        </div>
      )}
      {asking && (
        <div className="row">
          <input
            className="grow"
            value={reason}
            placeholder="删除理由（只有哈希上链，勿填敏感内容）"
            onChange={(e) => setReason(e.target.value)}
          />
          <button onClick={() => void props.onRedact(reason).then((ok) => ok && setAsking(false))}>发起提案</button>
          <button className="ghost" onClick={() => setAsking(false)}>取消</button>
        </div>
      )}
    </article>
  );
}

function Composer(props: { disabled: boolean; onSend: (body: string) => Promise<boolean> }) {
  const [text, setText] = useState("");
  const [sending, setSending] = useState(false);
  // Clear at once so nothing typed during signing is appended to this message;
  // restore the text if sending fails.
  const send = async () => {
    const body = text.trim();
    if (!body || sending) return;
    setSending(true);
    setText("");
    const ok = await props.onSend(body);
    if (!ok) setText((cur) => cur || body);
    setSending(false);
  };
  return (
    <div className="composer">
      <textarea
        value={text}
        readOnly={sending}
        placeholder={sending ? "正在签名并保存上一条…" : "输入消息，Enter 发送，Shift+Enter 换行"}
        maxLength={16384}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey) {
            e.preventDefault();
            void send();
          }
        }}
      />
      <button disabled={props.disabled || sending || !text.trim()} onClick={() => void send()}>{sending ? "发送中…" : "签名并发送"}</button>
    </div>
  );
}

function Proposals(props: {
  orders: OrderProposal[];
  onVoteOrder: (id: number, aFirst: boolean) => void;
  proposals: Proposal[];
  members: number;
  state: "loading" | "ok" | "error";
  onRetry: () => void;
  olderAvailable: boolean;
  onLoadOlder: () => void;
  byId: Map<string, Message>;
  me?: string;
  busy: boolean;
  onVote: (id: number, support: boolean) => void;
  onExecute: (id: number) => void;
  onTombstone: (id: number) => void;
}) {
  const failed = props.state === "error" && (
    <div className="banner error inline" role="alert">
      提案状态暂不可确认（链上读取失败）{props.proposals.length > 0 ? "，下面是上次成功读取的结果" : ""}。
      <button className="link" onClick={props.onRetry}>重试</button>
    </div>
  );
  const orders = props.orders.length > 0 && <OrderVotes orders={props.orders} byId={props.byId} busy={props.busy} onVote={props.onVoteOrder} />;
  if (props.proposals.length === 0) {
    if (props.state === "loading") return <p className="muted pad">正在读取删除提案…</p>;
    if (failed) return <div className="pad">{failed}{orders}</div>;
    return <><p className="muted pad">还没有删除提案。在已锚定的消息上点“申请删除”发起。</p>{orders}</>;
  }
  return (
    <>
    {failed && <div className="pad">{failed}</div>}
    <ul className="props">
      {[...props.proposals].reverse().map((p) => {
        const t = props.byId.get(p.target.toLowerCase());
        const needTomb = p.executed && t?.redaction === "authorized";
        const st = proposalState(p, props.proposals, props.members);
        return (
          <li key={p.id}>
            <div className="ph">
              <strong>提案 {p.id}</strong>
              <span className={`pill ${st.tone}`}>{st.label}</span>
            </div>
            <div className="muted small">目标 {short(p.target)} · 发起人 {short(p.proposer)} · 截止区块 {p.deadlineBlock}</div>
            <div className="votes">赞成 {p.yes} · 反对 {p.no}</div>
            {st.note && <div className="muted small">{st.note}</div>}
            <div className="row">
              {st.canVote && (
                <>
                  <button disabled={props.busy} onClick={() => props.onVote(p.id, true)}>赞成</button>
                  <button className="ghost" disabled={props.busy} onClick={() => props.onVote(p.id, false)}>反对</button>
                </>
              )}
              {st.canExecute && <button disabled={props.busy} onClick={() => props.onExecute(p.id)}>执行</button>}
              {needTomb && <button disabled={props.busy} onClick={() => props.onTombstone(p.id)}>签署墓碑</button>}
              {p.executed && t?.redaction === "applied" && <span className="muted small">墓碑已生效</span>}
              {p.executed && !t && (
                <span className="muted small">
                  目标消息不在已加载的历史中
                  {props.olderAvailable && <button className="link" onClick={props.onLoadOlder}>加载更早消息</button>}
                </span>
              )}
            </div>
          </li>
        );
      })}
    </ul>
    {orders}
    </>
  );
}

/** The room repository as Git sees it: heads, recent main commits, merges and anchored commits. */
/** On-chain votes on the order of concurrent message pairs. */
function OrderVotes(props: { orders: OrderProposal[]; byId: Map<string, Message>; busy: boolean; onVote: (id: number, aFirst: boolean) => void }) {
  const label = (id: Hex) => {
    const m = props.byId.get(id.toLowerCase());
    if (!m) return short(id);
    const t = m.body ?? (m.kind === 3 ? "删除墓碑" : "（正文不可用）");
    return `${short(m.author)}：${t.length > 20 ? t.slice(0, 20) + "…" : t}`;
  };
  return (
    <div className="orders">
      <h4 className="pad-x">排序投票（并发消息的先后）</h4>
      <ul className="props">
        {[...props.orders].reverse().map((o) => (
          <li key={o.id}>
            <div className="ph">
              <strong>排序 {o.id}</strong>
              <span className={`pill ${o.outcome ? "ok" : "warn"}`}>{o.outcome ? "已确定" : "投票中"}</span>
            </div>
            <div className="small">A　{label(o.a)}</div>
            <div className="small">B　{label(o.b)}</div>
            <div className="votes">A 在前 {o.forA} 票 · B 在前 {o.forB} 票 · 截止区块 {o.deadlineBlock}</div>
            {o.outcome !== 0 && <div className="muted small">结论：{o.outcome === 1 ? "A 在 B 之前" : "B 在 A 之前"}，各节点已按此排列</div>}
            {o.outcome === 0 && (
              <div className="row">
                <button disabled={props.busy} onClick={() => props.onVote(o.id, true)}>A 在前</button>
                <button className="ghost" disabled={props.busy} onClick={() => props.onVote(o.id, false)}>B 在前</button>
              </div>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

function GitPanel(props: { client: NodeClient; byId: Map<string, Message>; paused: boolean; onPause: (p: boolean) => void }) {
  const [view, setView] = useState<GitView | null>(null);
  const [err, setErr] = useState("");
  useEffect(() => {
    let live = true;
    const load = () => props.client.git().then((v) => { if (live) { setView(v); setErr(""); } }, (e: Error) => { if (live) setErr(e.message); });
    void load();
    const t = window.setInterval(() => void load(), 2000);
    return () => { live = false; window.clearInterval(t); };
  }, [props.client]);
  if (err) return <p className="pad error-text">读取 Git 仓库失败：{err}</p>;
  if (!view) return <p className="pad muted">正在读取 Git 仓库…</p>;
  const cpAt = new Map(view.checkpoints.map((c) => [c.commit, c]));
  const describe = (f: string) => {
    const m = f.match(/^events\/(0x[0-9a-f]{40})\/(\d+)\/(0x[0-9a-f]{64})\.json$/);
    if (!m) return f;
    const msg = props.byId.get(m[3]);
    const what = msg ? (msg.kind === 3 ? "删除墓碑" : msg.body ?? (msg.redaction !== "none" ? "（正文已删除）" : "")) : "";
    return `${short(m[1])} #${m[2]}${what ? `：${what.length > 18 ? what.slice(0, 18) + "…" : what}` : ""}`;
  };
  return (
    <div className="pad git">
      <p className="muted small">每条签名事件是 main 分支上的一次提交，文件路径为 events/作者/序号/eventId.json；正文另存在 content 分支。两个节点各自提交后再同步，会产生合并提交，签名不受影响。</p>
      <dl className="heads">
        <dt>本地 main</dt><dd className="sha">{view.head.slice(0, 10)}</dd>
        <dt>远端 origin/main</dt><dd className="sha">{view.remoteHead ? view.remoteHead.slice(0, 10) : "—"}{view.remoteHead === view.head ? "（与本地一致）" : ""}</dd>
        <dt>未推送提交</dt><dd>{view.unpushed}</dd>
        <dt>content 分支</dt><dd>第 {view.contentGeneration} 代 · 已删除正文 {view.contentDeleted} 条</dd>
      </dl>
      <div className="row">
        {props.paused
          ? <button onClick={() => props.onPause(false)}>恢复同步（重新联网）</button>
          : <button className="ghost" onClick={() => props.onPause(true)}>暂停同步（模拟断网）</button>}
      </div>
      <h4>main 分支最近提交</h4>
      <ul>
        {view.commits.map((c) => {
          const cp = cpAt.get(c.sha.toLowerCase());
          const merge = c.parents.length > 1;
          return (
            <li key={c.sha} className={merge ? "merge" : c.pushed ? "" : "local"}>
              <span className="sha">{c.sha.slice(0, 8)}</span> <span className="muted small">{time(c.time)}</span>
              {merge && <span className="tag mg">合并提交（{c.parents.map((x) => x.slice(0, 6)).join(" + ")}）</span>}
              {c.parents.length === 0 && <span className="tag">初始提交</span>}
              {!c.pushed && <span className="tag loc">仅在本地</span>}
              {cp && <span className="tag cp">检查点 #{cp.index} 锚定于此（{cp.count} 个事件，区块 {cp.blockNo}）</span>}
              {c.files.map((f) => <div key={f} className="file" title={f}>{describe(f)}</div>)}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

function RoomPanel({ status, onAnchor }: { status: Status; onAnchor: () => void }) {
  return (
    <div className="pad room">
      <dl>
        <dt>健康度</dt>
        <dd>{healthLabel[status.health]}{status.chainError ? `（${status.chainError}）` : ""}</dd>
        <dt>链</dt>
        <dd>{status.chainId}</dd>
        <dt>RoomRegistry</dt>
        <dd className="mono">{status.contracts.registry}</dd>
        <dt>AnchorLog</dt>
        <dd className="mono">{status.contracts.anchorLog}</dd>
        <dt>Governance</dt>
        <dd className="mono">{status.contracts.governance}</dd>
        <dt>同步</dt>
        <dd>{status.sync.error ?? (status.sync.pushed ? "已推送" : "—")}</dd>
      </dl>
      {status.isAnchorNode && <button onClick={onAnchor}>立即锚定（演示）</button>}
      <h4>报警</h4>
      {status.alerts.length === 0 ? <p className="muted">无</p> : (
        <ul className="alerts">{status.alerts.map((a, i) => <li key={i} className={a.level}>{a.code}：{a.detail}</li>)}</ul>
      )}
      {status.quarantine && status.quarantine.length > 0 && (
        <>
          <h4>已隔离的远端事件</h4>
          <ul className="alerts">{status.quarantine.map((q) => <li key={q.path} className="critical">{q.path}：{q.reason}</li>)}</ul>
        </>
      )}
    </div>
  );
}

function SettingsDialog(props: { initial: NodeSettings; onSave: (s: NodeSettings) => void; onClose: () => void }) {
  const [url, setUrl] = useState(props.initial.url);
  const [token, setToken] = useState(props.initial.token);
  return (
    <div className="modal" role="dialog" aria-modal="true">
      <div className="card">
        <h3>连接本地节点</h3>
        <label>
          节点地址
          <input value={url} onChange={(e) => setUrl(e.target.value.trim())} />
        </label>
        <label>
          访问令牌
          <input value={token} onChange={(e) => setToken(e.target.value.trim())} placeholder="见节点数据目录 state/api-token" />
        </label>
        <p className="muted small">令牌只保存在本浏览器。节点只监听本机地址，并校验页面来源。</p>
        <div className="row end">
          <button className="ghost" onClick={props.onClose}>取消</button>
          <button disabled={!url || !token} onClick={() => props.onSave({ url: url.replace(/\/$/, ""), token })}>保存</button>
        </div>
      </div>
    </div>
  );
}
