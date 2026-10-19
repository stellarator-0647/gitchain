import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type { Hex } from "viem";
import { NodeClient, NodeError, type NodeSettings } from "./api";
import { governance, hasWallet, signDraft } from "./wallet";
import type { Draft, Message, Proposal, Status } from "./types";
import { contentLabel, evidenceLabel, healthLabel, redactionLabel, short, time } from "./labels";
import { VerifyPanel } from "./VerifyPanel";
import { createDelegation, currentDelegate, forgetDelegate, signWithDelegate } from "./delegate";

type Mode = "wallet" | "delegate" | "dev";
type Tab = "proposals" | "room" | "verify";

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
      const [st, h, ps] = await Promise.all([client.status(), client.history(1), client.proposals().catch(() => [])]);
      setStatus({ ...st, alerts: st.alerts ?? [] });
      setMessages(h.messages ?? []);
      setProposals(ps ?? []);
      setConnError("");
    } catch (e) {
      setConnError((e as Error).message);
    }
  }, [client, settings.token]);

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


  useEffect(() => {
    // Recent Chromium returns a Promise from scrollIntoView; never return it to React.
    void listEnd.current?.scrollIntoView({ block: "end" });
  }, [messages.length]);

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
      setError(`${label}失败：${err.shortMessage ?? err.message}`);
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

  const gov = async (fn: "proposeRedaction" | "vote" | "execute", args: { target?: Hex; reason?: string; id?: number; support?: boolean }) => {
    if (mode === "dev") {
      if (fn === "proposeRedaction") return client.devPropose(args.target!, args.reason ?? "");
      if (fn === "vote") return client.devVote(args.id!, args.support!);
      return client.devExecute(args.id!);
    }
    const st = status!;
    const { keccak256, toHex } = await import("viem");
    if (fn === "proposeRedaction") return governance(st, fn, [st.room, args.target!, keccak256(toHex(args.reason ?? ""))]);
    if (fn === "vote") return governance(st, fn, [BigInt(args.id!), args.support!]);
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
            <span className={`pill ${status.sync.error && !status.sync.error.startsWith("push") ? "bad" : status.sync.error ? "warn" : "ok"}`} title={status.sync.error ?? ""}>
              {status.sync.error
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
      {mode === "dev" && status?.devSigner && (
        <div className="banner dev">测试签名模式：消息和投票由节点用测试私钥签名，只用于本地演示。</div>
      )}

      <main className="layout">
        <section className="chat">
          <div className="messages">
            {messages.length === 0 && <p className="muted empty">还没有消息。</p>}
            {messages.map((m) => (
              <MessageItem
                key={m.eventId}
                m={m}
                mine={!!status && m.author.toLowerCase() === status.wallet.toLowerCase()}
                target={m.target ? byId.get(m.target.toLowerCase()) : undefined}
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
            <button className={tab === "verify" ? "on" : ""} onClick={() => setTab("verify")}>举证验证</button>
          </nav>
          {tab === "proposals" && (
            <Proposals
              proposals={proposals}
              byId={byId}
              me={status?.wallet}
              busy={!!busy}
              onVote={(id, support) => run("投票", async () => void (await gov("vote", { id, support })))}
              onExecute={(id) => run("执行提案", async () => void (await gov("execute", { id })))}
              onTombstone={(id) =>
                run("签署墓碑", async () => {
                  await signAndCommit(await client.tombstoneDraft(id));
                  return "墓碑已签署，等待锚定";
                })
              }
            />
          )}
          {tab === "room" && status && (
            <RoomPanel status={status} onAnchor={() => run("锚定", async () => void (await client.anchorNow()))} />
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

function MessageItem(props: { m: Message; mine: boolean; target?: Message; onProof: (withContent: boolean) => void; onRedact: (reason: string) => Promise<boolean> }) {
  const { m, mine, target } = props;
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
        {m.evidence === "anchored" && (
          <span className="acts">
            <button className="link" onClick={() => props.onProof(m.content === "available")}>导出证明</button>
            {m.kind === 1 && m.redaction === "none" && <button className="link" onClick={() => setAsking(!asking)}>申请删除</button>}
          </span>
        )}
      </div>
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
  proposals: Proposal[];
  byId: Map<string, Message>;
  me?: string;
  busy: boolean;
  onVote: (id: number, support: boolean) => void;
  onExecute: (id: number) => void;
  onTombstone: (id: number) => void;
}) {
  if (props.proposals.length === 0) return <p className="muted pad">还没有删除提案。在已锚定的消息上点“申请删除”发起。</p>;
  return (
    <ul className="props">
      {[...props.proposals].reverse().map((p) => {
        const t = props.byId.get(p.target.toLowerCase());
        const needTomb = p.executed && t?.redaction === "authorized";
        return (
          <li key={p.id}>
            <div className="ph">
              <strong>提案 {p.id}</strong>
              <span className={`pill ${p.executed ? "ok" : "warn"}`}>{p.executed ? "已执行" : "投票中"}</span>
            </div>
            <div className="muted small">目标 {short(p.target)} · 发起人 {short(p.proposer)} · 截止区块 {p.deadlineBlock}</div>
            <div className="votes">赞成 {p.yes} · 反对 {p.no}</div>
            <div className="row">
              {!p.executed && (
                <>
                  <button disabled={props.busy} onClick={() => props.onVote(p.id, true)}>赞成</button>
                  <button className="ghost" disabled={props.busy} onClick={() => props.onVote(p.id, false)}>反对</button>
                  <button className="ghost" disabled={props.busy} onClick={() => props.onExecute(p.id)}>执行</button>
                </>
              )}
              {needTomb && <button disabled={props.busy} onClick={() => props.onTombstone(p.id)}>签署墓碑</button>}
              {p.executed && t?.redaction === "applied" && <span className="muted small">墓碑已生效</span>}
            </div>
          </li>
        );
      })}
    </ul>
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
