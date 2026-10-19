// Independent check of an exported proof bundle. It uses the trusted
// deployment manifest the user supplies (not the node's word) and reads only
// the chain; it works with the node and Git server switched off.
import { useState } from "react";
import { verifyBundle, type Manifest, type ProofBundle, type Report } from "@gitchain/verifier";

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
      {report && (
        <div className={`report ${report.ok ? "pass" : "fail"}`}>
          <ol>
            {report.steps.map((s, i) => (
              <li key={i} className={s.status}>
                <span className="st">{s.status === "pass" ? "通过" : s.status === "fail" ? "失败" : "无法判断"}</span>
                <span>{s.name}</span>
                {s.detail && <span className="muted small"> {s.detail}</span>}
              </li>
            ))}
          </ol>
          <p className="conclusion">{report.conclusion}</p>
        </div>
      )}
    </div>
  );
}
