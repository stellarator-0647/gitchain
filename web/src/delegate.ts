// Short-lived browser key ("delegate"). The wallet signs one Delegation; the
// delegate then signs messages without wallet pop-ups. The key lives only in
// this tab's sessionStorage and is useless after the delegation ends.
import { hashTypedData, isAddressEqual, type Address, type Hex } from "viem";
import { generatePrivateKey, privateKeyToAccount, sign } from "viem/accounts";
import type { NodeClient } from "./api";
import type { Draft, Status, TypedData } from "./types";
import { validateDelegationDraft } from "./delegation-policy";
import { signTypedDataWithWallet } from "./wallet";

type Stored = { pk: Hex; address: Address; validUntil: number; wallet: Address; room: Hex; chainId: number; registry: Address };

const storageKey = (st: Status) => `gitchain.delegate.${st.chainId}.${st.contracts.registry}.${st.room}.${st.wallet}`.toLowerCase();

function load(st: Status): Stored | null {
  try {
    const raw = sessionStorage.getItem(storageKey(st));
    return raw ? (JSON.parse(raw) as Stored) : null;
  } catch {
    return null;
  }
}

function save(st: Status, v: Stored) {
  try {
    sessionStorage.setItem(storageKey(st), JSON.stringify(v));
  } catch {
    /* without storage the key lives only in memory for this page */
  }
  memory = v;
}

let memory: Stored | null = null;

/** The usable delegate for this tab, if the node also holds its delegation. */
export function currentDelegate(st: Status): Stored | null {
  const v = load(st) ?? memory;
  const now = Date.now() / 1000;
  if (!v || v.validUntil - 60 <= now || v.chainId !== st.chainId || !v.registry || !isAddressEqual(v.registry, st.contracts.registry) || !isAddressEqual(v.wallet, st.wallet) || v.room.toLowerCase() !== st.room.toLowerCase()) return null;
  if (!st.delegation || !isAddressEqual(st.delegation.delegate, v.address)) return null;
  return v;
}

/** Creates a fresh delegate key and asks the wallet to authorise it once. */
export async function createDelegation(client: NodeClient, st: Status, seconds = 3600): Promise<Stored> {
  const pk = generatePrivateKey();
  const acct = privateKeyToAccount(pk);
  const d = await client.delegationDraft(acct.address, seconds);
  validateDelegationDraft(d, st, acct.address, seconds);
  const msg = d.delegation;
  if (!isAddressEqual(msg.wallet, st.wallet) || msg.room.toLowerCase() !== st.room.toLowerCase() || !isAddressEqual(msg.delegate, acct.address)) {
    throw new Error("节点返回的授权内容与本页不一致，已拒绝");
  }
  const walletSig = await signTypedDataWithWallet(d.typedData, st.wallet, st.chainId, st.rpc);
  await client.setDelegation({ delegation: msg, walletSig });
  const v: Stored = { pk, address: acct.address, validUntil: msg.validUntil, wallet: st.wallet, room: st.room, chainId: st.chainId, registry: st.contracts.registry };
  save(st, v);
  return v;
}

/** Recomputes the EIP-712 digest locally so the delegate never signs a value it did not check. */
function digestOf(td: TypedData): Hex {
  const m = td.message;
  return hashTypedData({
    domain: { name: td.domain.name, version: td.domain.version, chainId: Number(td.domain.chainId), verifyingContract: td.domain.verifyingContract as Address },
    types: { Event: td.types.Event },
    primaryType: "Event",
    message: { ...m, seq: BigInt(m.seq), ts: BigInt(m.ts), kind: Number(m.kind) },
  } as never);
}

/** Signs a node-prepared message draft with the delegate key. */
export async function signWithDelegate(d: Draft, st: Status, del: Stored): Promise<Hex> {
  const td = d.typedData;
  if (td.domain.name !== "GitChain" || td.domain.version !== "2" || !isAddressEqual(td.domain.verifyingContract as Address, st.contracts.registry) || Number(td.domain.chainId) !== st.chainId || td.message.room.toLowerCase() !== st.room.toLowerCase() ||
      !isAddressEqual(td.message.author as Address, st.wallet)) {
    throw new Error("草稿的链、房间或作者与本页不一致，拒绝签名");
  }
  if (!["1", "2"].includes(td.message.kind)) throw new Error("该类事件必须由钱包本人签名");
  const digest = digestOf(td);
  if (digest.toLowerCase() !== d.eventId.toLowerCase()) throw new Error("本地重算的签名摘要与节点不一致，拒绝签名");
  return sign({ hash: digest, privateKey: del.pk, to: "hex" });
}

export function forgetDelegate(st: Status) {
  try {
    sessionStorage.removeItem(storageKey(st));
  } catch {
    /* ignore */
  }
  memory = null;
}
