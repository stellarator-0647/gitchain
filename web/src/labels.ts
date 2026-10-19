import type { Content, Evidence, Redaction, Status } from "./types";

export const evidenceLabel: Record<Evidence, string> = {
  pending: "待锚定",
  anchored: "已锚定",
  invalid: "无效",
};

export const contentLabel: Record<Content, string> = {
  available: "正文可用",
  pending: "正文待同步",
  missing: "正文缺失",
  purged: "正文已清理",
};

export const redactionLabel: Record<Redaction, string> = {
  none: "",
  authorized: "删除已授权",
  applied: "已依提案删除",
};

export const healthLabel: Record<Status["health"], string> = {
  healthy: "正常",
  checkpoint_missing: "检查点数据缺失",
  checkpoint_invalid: "检查点无效",
  rpc_unavailable: "链暂不可读",
};

export const short = (h?: string, n = 6) => (h ? `${h.slice(0, n)}…${h.slice(-4)}` : "");

export const time = (ts: number) =>
  new Date(ts * 1000).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
