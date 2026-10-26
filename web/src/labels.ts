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

// Custom errors of the three contracts (4-byte selectors), as revert reasons surface them.
const CONTRACT_ERRORS: Record<string, string> = {
  "0x0b7b8dc6": "赞成票未过半（需要赞成票 × 2 > 成员数）",
  "0x55e6775b": "目标消息已被另一个提案删除",
  "0x0dc10197": "提案已经执行过",
  "0x66b6cb4a": "投票期已结束",
  "0x7c9a1cf9": "你已经投过票",
  "0x291fc442": "当前钱包不是房间成员",
  "0x5d5d3ca0": "提案不存在",
  "0x46c2cca1": "目标为空",
  "0x35bf5e38": "该 Git 提交已经锚定过（可能刚被自动锚定）",
  "0x934968cf": "不能对同一条消息排序",
  "0xbf112ecb": "这两条消息已有进行中的排序投票",
  "0x70f129b4": "这两条消息的先后已经由投票确定",
  "0xb838de96": "排序投票不存在",
};
const ERROR_NAMES: Record<string, string> = {
  NotEnoughVotes: CONTRACT_ERRORS["0x0b7b8dc6"], AlreadyRedacted: CONTRACT_ERRORS["0x55e6775b"],
  AlreadyExecuted: CONTRACT_ERRORS["0x0dc10197"], VotingClosed: CONTRACT_ERRORS["0x66b6cb4a"],
  AlreadyVoted: CONTRACT_ERRORS["0x7c9a1cf9"], NotMember: CONTRACT_ERRORS["0x291fc442"],
  OrderOpen: CONTRACT_ERRORS["0xbf112ecb"], AlreadyOrdered: CONTRACT_ERRORS["0x70f129b4"],
};

/** Adds a Chinese explanation to contract reverts and known node errors. */
export function explainError(msg: string): string {
  const sel = msg.match(/0x[0-9a-fA-F]{8}(?![0-9a-fA-F])/)?.[0]?.toLowerCase();
  const hit = (sel && CONTRACT_ERRORS[sel]) || Object.entries(ERROR_NAMES).find(([k]) => msg.includes(k))?.[1];
  if (hit) return `${hit}（${msg}）`;
  if (msg.includes("already has a tombstone")) return `该提案已有墓碑，无需重复签署（${msg}）`;
  return msg;
}

export const short = (h?: string, n = 6) => (h ? `${h.slice(0, n)}…${h.slice(-4)}` : "");

export const time = (ts: number) =>
  new Date(ts * 1000).toLocaleString("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
