// Client for the local node API. Every call carries the node's access token;
// the node only listens on loopback and checks the page Origin.
import type { AnchorResult, Delegation, DelegationDraft, Draft, GitView, History, OrderProposal, Proposal, Status } from "./types";
import type { Hex } from "viem";

export type NodeSettings = { url: string; token: string };

export class NodeError extends Error {
  code: string;
  status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.code = code;
    this.status = status;
  }
}

export class NodeClient {
  private s: NodeSettings;
  constructor(s: NodeSettings) {
    this.s = s;
  }

  private async call<T>(method: string, path: string, body?: unknown): Promise<T> {
    let res: Response;
    try {
      res = await fetch(`${this.s.url}/api${path}`, {
        method,
        signal: AbortSignal.timeout(30000),
        headers: { "X-GitChain-Token": this.s.token, ...(body ? { "Content-Type": "application/json" } : {}) },
        body: body ? JSON.stringify(body) : undefined,
      });
    } catch {
      throw new NodeError("node_unreachable", `无法连接本地节点 ${this.s.url}`, 0);
    }
    const data = await res.json().catch(() => ({}));
    if (!res.ok) throw new NodeError(data.error ?? "http_error", data.detail ?? data.error ?? res.statusText, res.status);
    return data as T;
  }

  status = () => this.call<Status>("GET", "/status");
  history = (page = 1) => this.call<History>("GET", `/history?page=${page}`);
  proposals = () => this.call<Proposal[]>("GET", "/proposals");
  orders = () => this.call<OrderProposal[]>("GET", "/orders");
  messageDraft = (body: string) => this.call<Draft>("POST", "/messages/draft", { body });
  tombstoneDraft = (proposalId: number) => this.call<Draft>("POST", "/events/tombstone-draft", { proposalId });
  commit = (draftId: string, sig: Hex) => this.call<{ eventId: Hex }>("POST", "/events/commit", { draftId, sig });
  cancelDraft = (id: string) => this.call<{ released: boolean }>("DELETE", `/events/draft/${id}`);
  proof = (eventId: Hex, content: boolean) => this.call<unknown>("GET", `/proof/${eventId}?content=${content ? 1 : 0}`);
  anchorNow = () => this.call<AnchorResult>("POST", "/anchor/now");
  git = () => this.call<GitView>("GET", "/git");
  pauseSync = (paused: boolean) => this.call<{ paused: boolean }>("POST", "/sync/pause", { paused });
  delegationDraft = (delegate: Hex, seconds: number) => this.call<DelegationDraft>("POST", "/delegations/draft", { delegate, seconds });
  setDelegation = (sd: { delegation: Delegation; walletSig: Hex }) => this.call<unknown>("POST", "/delegations", sd);
  clearDelegation = () => this.call<unknown>("DELETE", "/delegations");

  // TEST-key endpoints, present only when the node runs with -dev-key-env.
  devSign = (draftId: string) => this.call<{ eventId: Hex }>("POST", "/dev/sign", { draftId });
  devPropose = (target: Hex, reason: string) => this.call<{ proposalId: number }>("POST", "/dev/propose", { target, reason });
  devVote = (id: number, support: boolean) => this.call<unknown>("POST", "/dev/vote", { id, support });
  devExecute = (id: number) => this.call<unknown>("POST", "/dev/execute", { id });
  devProposeOrder = (a: Hex, b: Hex) => this.call<{ orderId: number }>("POST", "/dev/order/propose", { a, b });
  devVoteOrder = (id: number, aFirst: boolean) => this.call<unknown>("POST", "/dev/order/vote", { id, aFirst });

  /** Opens the push channel; returns a closer. Reconnects every 3 s. */
  subscribe(onEvent: (type: string) => void): () => void {
    let ws: WebSocket | null = null;
    let closed = false;
    let timer: number | undefined;
    const open = () => {
      const url = this.s.url.replace(/^http/, "ws") + `/ws?token=${encodeURIComponent(this.s.token)}`;
      ws = new WebSocket(url);
      ws.onmessage = (e) => {
        try {
          onEvent(JSON.parse(e.data).type);
        } catch {
          /* ignore malformed frames */
        }
      };
      ws.onclose = () => {
        if (!closed) timer = window.setTimeout(open, 3000);
      };
    };
    open();
    return () => {
      closed = true;
      window.clearTimeout(timer);
      ws?.close();
    };
  }
}
