// JSON shapes returned by the local node (internal/api, internal/verify).
import type { Address, Hex } from "viem";

export type Evidence = "pending" | "anchored" | "invalid";
export type Content = "available" | "pending" | "missing" | "purged";
export type Redaction = "none" | "authorized" | "applied";

export type Message = {
  eventId: Hex;
  author: Address;
  seq: number;
  ts: number;
  kind: number;
  target?: Hex;
  body?: string;
  evidence: Evidence;
  content: Content;
  redaction: Redaction;
  checkpointIndex?: number;
  anchorBlock?: number;
  proposalId?: number;
  reason?: string;
  /** Other authors' events neither side had seen when signing; order is this message relative to the peer once settled on chain. */
  concurrent?: { eventId: Hex; order?: "before" | "after"; orderProposal?: number }[];
};

/** Governance.orderProposal: outcome 0 open, 1 a before b, 2 b before a. */
export type OrderProposal = {
  id: number;
  room: Hex;
  a: Hex;
  b: Hex;
  proposer: Address;
  createdBlock: number;
  deadlineBlock: number;
  forA: number;
  forB: number;
  outcome: 0 | 1 | 2;
};

export type Alert = { level: "warn" | "critical"; code: string; detail: string };

export type Checkpoint = {
  index: number;
  count: number;
  blockNo: number;
  blockTime: number;
  root: Hex;
  txHash: Hex;
};

export type SyncReport = {
  fetched: boolean;
  mainMerged: boolean;
  mainForwarded: boolean;
  contentRebuilt: boolean;
  pushed: boolean;
  unpushed: number;
  error?: string;
};

export type Status = {
  room: Hex;
  wallet: Address;
  chainId: number;
  rpc: string;
  contracts: { registry: Address; anchorLog: Address; governance: Address };
  isAnchorNode: boolean;
  health: "healthy" | "checkpoint_missing" | "checkpoint_invalid" | "rpc_unavailable";
  chainError?: string;
  sync: SyncReport;
  quarantine?: { path: string; reason: string }[];
  alerts: Alert[];
  latestCheckpoint?: Checkpoint;
  devSigner: boolean;
  delegation?: { delegate: Address; validUntil: number; id: Hex };
  members: number;
  syncPaused: boolean;
};

export type GitCommit = { sha: string; parents: string[]; time: number; subject: string; files: string[]; pushed: boolean };
export type GitView = {
  head: string;
  remoteHead: string;
  unpushed: number;
  syncPaused: boolean;
  contentHead: string;
  contentGeneration: number;
  contentDeleted: number;
  commits: GitCommit[];
  checkpoints: { index: number; commit: string; count: number; blockNo: number }[];
};

export type AnchorResult = { anchored: boolean; skipped: "" | "nothing_new" | "unpushed"; latest?: Checkpoint };

export type TypedData = {
  types: Record<string, { name: string; type: string }[]>;
  primaryType: string;
  domain: Record<string, string>;
  message: Record<string, string>;
};

export type Draft = { draftId: string; eventId: Hex; typedData: TypedData };

export type Delegation = { room: Hex; wallet: Address; delegate: Address; validFrom: number; validUntil: number; kinds: number };
export type DelegationDraft = { delegation: Delegation; id: Hex; typedData: TypedData };

export type Proposal = {
  id: number;
  room: Hex;
  target: Hex;
  proposer: Address;
  createdBlock: number;
  deadlineBlock: number;
  yes: number;
  no: number;
  executed: boolean;
};

export type History = { total: number; page: number; head?: string; messages: Message[] };
