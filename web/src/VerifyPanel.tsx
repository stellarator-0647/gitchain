// Independent check of an exported proof bundle. It uses the trusted
// deployment manifest the user supplies (not the node's word) and reads only
// the chain; it works with the node and Git server switched off.
import { useState } from "react";
import { verifyBundle, type Manifest, type ProofBundle, type Report } from "@gitchain/verifier";

// Chinese names for the verifier's step names.
const STEP: Record<string, string> = {
  format: "格式与版本",
  deployment: "部署一致（链与合约）",
  "event: signature": "签名",
  "event: delegation": "短期授权",
  "event id": "事件编号 eventId",
  room: "房间",
  membership: "成员身份",
  "event: inclusion": "锚定证明",
  "event: checkpoint": "链上检查点",
  "event: delegation time": "授权有效期",
  "event: Merkle path": "Merkle 路径",
  content: "正文与签名承诺",
  deletion: "删除提案（链上）",
  tombstone: "墓碑",
  "tombstone: signature": "墓碑签名",
  "tombstone: delegation": "墓碑授权",
  "tombstone: membership": "墓碑签署人成员身份",
  "tombstone: inclusion": "墓碑锚定证明",
  "tombstone: checkpoint": "墓碑检查点",
  "tombstone: delegation time": "墓碑授权有效期",
  "tombstone: Merkle path": "墓碑 Merkle 路径",
};

const CONTENT: Record<NonNullable<Report["facts"]["content"]>, string> = {
  matched: "证明包中的正文与签名承诺一致（未被改动）",
  mismatch: "证明包中的正文与签名承诺不一致（已被改动）",
  undisclosed: "未附正文，只证明事件存在与锚定",
  deleted: "正文已按投票删除，证明包不含正文",
};

const fmt = (ts?: number) => (ts ? new Date(ts * 1000).toLocaleString("zh-CN") : "—");

/** Plain-language summary of a report, shown above the step list. */
function Summary({ r }: { r: Report }) {
  const f = r.facts ?? {};
  const failed = r.steps.filter((x) => x.status === "fail").map((x) => STEP[x.name] ?? x.name);
  const unknown = r.steps.filter((x) => x.status === "unknown").map((x) => STEP[x.name] ?? x.name);
  return (
    <div className={`summary ${r.ok ? "pass" : "fail"}`}>
      <h4>{r.ok ? "✓ 验证通过" : unknown.length && !failed.length ? "？ 无法判断" : "✗ 验证未通过"}</h4>
      <dl>
        {failed.length > 0 && <><dt>未通过</dt><dd>{failed.join("、")}</dd></>}
        {unknown.length > 0 && <><dt>无法判断</dt><dd>{unknown.join("、")}（链读取失败，不能当作通过）</dd></>}
        <dt>签名者</dt><dd className="mono">{f.author ?? "—"}{f.delegate ? `（由临时密钥 ${f.delegate} 代签）` : ""}</dd>
        <dt>存在时间</dt><dd>{f.blockNo !== undefined ? `不晚于 ${fmt(f.blockTime)}（检查点 #${f.checkpointIndex}，区块 ${f.blockNo}）` : "未得到链上检查点，无法证明存在时间"}</dd>
        <dt>正文</dt><dd>{f.content ? CONTENT[f.content] : "—"}</dd>
        {f.proposalId !== undefined && <><dt>删除依据</dt><dd>链上提案 {f.proposalId} 已执行；墓碑签署人 <span className="mono">{f.tombstoneBy}</span></dd></>}
        <dt>事件编号</dt><dd className="mono">{f.eventId ?? "—"}</dd>
      </dl>
    </div>
  );
}

async function readJSON<T>(f: File): Promise<T> {
  return JSON.parse(await f.text()) as T;
}

export function VerifyPanel() {
  const [proof, setProof] = useState<File | null>(null);
  const [manifest, setManifest] = useState<File | null>(null);
  const [report, setReport] = useState<Report | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const verify = async () => {
    if (!proof || !manifest) return;
    setBusy(true);
    setErr("");
    setReport(null);
    try {
      setReport(await verifyBundle(await readJSON<ProofBundle>(proof), await readJSON<Manifest>(manifest)));
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="pad verify">
      <p className="muted small">选择证明包和可信部署清单（如 deployments/31337.json）。验证只读链，不连接节点或 Git。</p>
      <label>
        证明包
        <input type="file" accept="application/json" onChange={(e) => setProof(e.target.files?.[0] ?? null)} />
      </label>
      <label>
        可信部署清单
        <input type="file" accept="application/json" onChange={(e) => setManifest(e.target.files?.[0] ?? null)} />
      </label>
      <button disabled={!proof || !manifest || busy} onClick={() => void verify()}>{busy ? "验证中…" : "验证"}</button>
      {err && <p className="error-text">{err}</p>}
      {report && <Summary r={report} />}
      {report && (
        <div className={`report ${report.ok ? "pass" : "fail"}`}>
          <ol>
            {report.steps.map((s, i) => (
              <li key={i} className={s.status}>
                <span className="st">{s.status === "pass" ? "通过" : s.status === "fail" ? "失败" : "无法判断"}</span>
                <span>{STEP[s.name] ?? s.name}</span>
                {s.detail && <span className="muted small"> {s.detail}</span>}
              </li>
            ))}
          </ol>
          <p className="conclusion">英文结论：{report.conclusion}</p>
        </div>
      )}
    </div>
  );
}
