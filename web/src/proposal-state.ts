import type { Proposal } from "./types.ts";

/** What a proposal can still become, from on-chain counts and the room's other proposals. */
export function proposalState(p: Proposal, all: Proposal[], members: number): { label: string; tone: string; note?: string; canVote: boolean; canExecute: boolean } {
  if (p.executed) return { label: "已执行", tone: "ok", canVote: false, canExecute: false };
  const winner = all.find((q) => q.executed && q.id !== p.id && q.target.toLowerCase() === p.target.toLowerCase());
  if (winner) return { label: "已失效", tone: "muted", note: `目标消息已被提案 ${winner.id} 删除，本提案不能再执行`, canVote: false, canExecute: false };
  const passed = p.yes * 2 > members;
  if (!passed && (members - p.no) * 2 <= members) return { label: "已否决", tone: "bad", note: `反对 ${p.no} 票，剩余票数已不可能过半`, canVote: false, canExecute: false };
  if (passed) return { label: "可执行", tone: "ok", note: "赞成已过半，任一成员可点“执行”", canVote: false, canExecute: true };
  const need = Math.floor(members / 2) + 1 - p.yes;
  return { label: "投票中", tone: "warn", note: `还需 ${need} 票赞成（${members} 人房间需过半）`, canVote: true, canExecute: false };
}
