// Independent GitChain proof-bundle verifier (docs/protocol.md §6.6).
// It recomputes everything locally and reads only the chain named in a
// trusted deployment manifest. A bundle may reference a deployment but can
// never override it. RPC failures yield "unknown", never "pass".
import {
  concat,
  createPublicClient,
  hashDomain,
  hashTypedData,
  hexToBigInt,
  http,
  isAddressEqual,
  keccak256,
  recoverAddress,
  stringToBytes,
  toHex,
  type Address,
  type Hex,
  type PublicClient,
} from "viem";

export type Manifest = {
  chainId: number;
  rpc: string;
  registry: Address;
  anchorLog: Address;
  governance: Address;
};

export type Event = {
  room: Hex;
  author: Address;
  seq: number;
  authorPrev: Hex;
  seen: Hex;
  ts: number;
  kind: number;
  target: Hex;
  data1: Hex;
  data2: Hex;
};

export type Delegation = {
  room: Hex;
  wallet: Address;
  delegate: Address;
  validFrom: number;
  validUntil: number;
  kinds: number;
};
export type SignedDelegation = { delegation: Delegation; walletSig: Hex };
export type SignedEvent = { event: Event; sig: Hex; delegation?: SignedDelegation | null };
export type Blob = { salt: Hex; body: string };
export type Inclusion = { checkpointIndex: number; root: Hex; count: number; leafIndex: number; proof: Hex[] };

export type ProofBundle = {
  version: number;
  chainId: number;
  room: Hex;
  contracts: { registry: Address; anchorLog: Address; governance: Address };
  event: SignedEvent;
  eventId: Hex;
  content: Blob | null;
  inclusion: Inclusion | null;
  redaction: { proposalId: number; tombstone: SignedEvent; tombstoneInclusion: Inclusion | null } | null;
};

export const KIND = { message: 1, receipt: 2, tombstone: 3 } as const;

export const EVENT_TYPES = {
  Event: [
    { name: "room", type: "bytes32" },
    { name: "author", type: "address" },
    { name: "seq", type: "uint64" },
    { name: "authorPrev", type: "bytes32" },
    { name: "seen", type: "bytes32" },
    { name: "ts", type: "uint64" },
    { name: "kind", type: "uint8" },
    { name: "target", type: "bytes32" },
    { name: "data1", type: "bytes32" },
    { name: "data2", type: "bytes32" },
  ],
} as const;

export const DELEGATION_TYPES = {
  Delegation: [
    { name: "room", type: "bytes32" },
    { name: "wallet", type: "address" },
    { name: "delegate", type: "address" },
    { name: "validFrom", type: "uint64" },
    { name: "validUntil", type: "uint64" },
    { name: "kinds", type: "uint256" },
  ],
} as const;

/** Kinds a delegate may sign: messages (1) and receipts (2). */
export const DELEGABLE_KINDS = (1 << 1) | (1 << 2);
export const MAX_DELEGATION_SECONDS = 2 * 60 * 60;

export function domainOf(m: Pick<Manifest, "chainId" | "registry">) {
  return { name: "GitChain", version: "2", chainId: m.chainId, verifyingContract: m.registry } as const;
}

export function domainSeparator(m: Pick<Manifest, "chainId" | "registry">): Hex {
  return hashDomain({
    domain: { ...domainOf(m), chainId: BigInt(m.chainId) },
    types: { EIP712Domain: [
      { name: "name", type: "string" },
      { name: "version", type: "string" },
      { name: "chainId", type: "uint256" },
      { name: "verifyingContract", type: "address" },
    ] },
  });
}

/** eventId = keccak256(0x1901 || domainSeparator || hashStruct(event)). */
export function eventId(e: Event, m: Pick<Manifest, "chainId" | "registry">): Hex {
  return hashTypedData({
    domain: domainOf(m),
    types: EVENT_TYPES,
    primaryType: "Event",
    message: { ...e, seq: BigInt(e.seq), ts: BigInt(e.ts) },
  });
}

/** EIP-712 digest of a delegation (what the wallet signs). */
export function delegationId(d: Delegation, m: Pick<Manifest, "chainId" | "registry">): Hex {
  return hashTypedData({
    domain: domainOf(m),
    types: DELEGATION_TYPES,
    primaryType: "Delegation",
    message: { ...d, validFrom: BigInt(d.validFrom), validUntil: BigInt(d.validUntil), kinds: BigInt(d.kinds) },
  });
}

const HALF_N = 0x7fffffffffffffffffffffffffffffff5d576e7357a4501ddfe92f46681b20a0n;

/** Throws unless sig is 65 bytes, low-s, v in {27, 28}. */
export function assertCanonicalSig(sig: Hex): void {
  if (!/^0x[0-9a-fA-F]{130}$/.test(sig)) throw new Error("signature must be 65 bytes");
  const s = hexToBigInt(`0x${sig.slice(66, 130)}`);
  const v = parseInt(sig.slice(130, 132), 16);
  if (v !== 27 && v !== 28) throw new Error("signature v must be 27 or 28");
  if (s === 0n || s > HALF_N) throw new Error("non-canonical signature (high s)");
}

export async function recoverSigner(id: Hex, sig: Hex): Promise<Address> {
  assertCanonicalSig(sig);
  return recoverAddress({ hash: id, signature: sig });
}

/** keccak256(0x00 || eventId || keccak256(sig)) */
export function leaf(id: Hex, sig: Hex): Hex {
  return keccak256(concat(["0x00", id, keccak256(sig)]));
}

/** keccak256(0x01 || eventId || keccak256(sig) || delegationId) */
export function delegatedLeaf(id: Hex, sig: Hex, delId: Hex): Hex {
  return keccak256(concat(["0x01", id, keccak256(sig), delId]));
}

/** Checkpoint leaf of a signed event. */
export function eventLeaf(se: SignedEvent, id: Hex, m: Pick<Manifest, "chainId" | "registry">): Hex {
  return se.delegation ? delegatedLeaf(id, se.sig, delegationId(se.delegation.delegation, m)) : leaf(id, se.sig);
}

function hashPair(a: Hex, b: Hex): Hex {
  return hexToBigInt(a) <= hexToBigInt(b) ? keccak256(concat([a, b])) : keccak256(concat([b, a]));
}

export function verifyProof(l: Hex, proof: Hex[], root: Hex): boolean {
  let h = l;
  for (const p of proof) h = hashPair(h, p);
  return h.toLowerCase() === root.toLowerCase();
}

/** keccak256(salt || utf8(body)) */
export function contentCommit(salt: Hex, body: string): Hex {
  return keccak256(concat([salt, toHex(stringToBytes(body))]));
}

export function proposalIdWord(id: number | bigint): Hex {
  return toHex(BigInt(id), { size: 32 });
}

const registryAbi = [
  { type: "function", name: "roomInfo", stateMutability: "view", inputs: [{ name: "room", type: "bytes32" }],
    outputs: [{ type: "tuple", components: [
      { name: "creator", type: "address" }, { name: "anchorWriter", type: "address" },
      { name: "createdBlock", type: "uint64" }, { name: "memberCount", type: "uint32" }] }] },
  { type: "function", name: "isMember", stateMutability: "view",
    inputs: [{ name: "room", type: "bytes32" }, { name: "who", type: "address" }], outputs: [{ type: "bool" }] },
] as const;

const anchorLogAbi = [
  { type: "function", name: "checkpoint", stateMutability: "view",
    inputs: [{ name: "room", type: "bytes32" }, { name: "index", type: "uint256" }],
    outputs: [{ type: "tuple", components: [
      { name: "gitCommit", type: "bytes32" }, { name: "root", type: "bytes32" }, { name: "count", type: "uint32" },
      { name: "by", type: "address" }, { name: "blockNo", type: "uint64" }, { name: "blockTime", type: "uint64" },
      { name: "prevIndex", type: "uint256" }] }] },
  { type: "function", name: "checkpointCount", stateMutability: "view",
    inputs: [{ name: "room", type: "bytes32" }], outputs: [{ type: "uint256" }] },
] as const;

const governanceAbi = [
  { type: "function", name: "proposal", stateMutability: "view", inputs: [{ name: "id", type: "uint256" }],
    outputs: [{ type: "tuple", components: [
      { name: "room", type: "bytes32" }, { name: "targetEventId", type: "bytes32" }, { name: "reasonHash", type: "bytes32" },
      { name: "proposer", type: "address" }, { name: "createdBlock", type: "uint64" }, { name: "deadlineBlock", type: "uint64" },
      { name: "yes", type: "uint32" }, { name: "no", type: "uint32" }, { name: "executed", type: "bool" }] }] },
] as const;

export type Step = { name: string; status: "pass" | "fail" | "unknown"; detail: string };
export type Report = { ok: boolean; steps: Step[]; conclusion: string };

class Steps {
  list: Step[] = [];
  pass(name: string, detail = "") { this.list.push({ name, status: "pass", detail }); }
  fail(name: string, detail: string) { this.list.push({ name, status: "fail", detail }); }
  unknown(name: string, detail: string) { this.list.push({ name, status: "unknown", detail }); }
  get ok() { return this.list.every((s) => s.status === "pass"); }
}

const eq = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

async function checkSigned(s: Steps, label: string, se: SignedEvent, m: Manifest): Promise<Hex | null> {
  const id = eventId(se.event, m);
  try {
    const who = await recoverSigner(id, se.sig);
    if (!se.delegation) {
      if (!isAddressEqual(who, se.event.author)) {
        s.fail(`${label}: signature`, `recovers ${who}, event claims ${se.event.author}`);
        return null;
      }
      s.pass(`${label}: signature`, `signed by ${who}`);
      return id;
    }
    const d = se.delegation.delegation;
    const problem =
      !isAddressEqual(d.wallet, se.event.author) ? "delegation was not issued by the event author"
      : !eq(d.room, se.event.room) ? "delegation belongs to another room"
      : d.validUntil <= d.validFrom || d.validUntil - d.validFrom > MAX_DELEGATION_SECONDS ? "delegation lifetime is empty or exceeds 2 hours"
      : d.kinds === 0 || (d.kinds & ~DELEGABLE_KINDS) !== 0 || (d.kinds & (1 << se.event.kind)) === 0 ? "delegation does not allow this event kind"
      : isAddressEqual(d.delegate, d.wallet) ? "delegate equals wallet"
      : !isAddressEqual(who, d.delegate) ? `recovers ${who}, delegation names ${d.delegate}`
      : "";
    if (problem) {
      s.fail(`${label}: signature`, problem);
      return null;
    }
    const issuer = await recoverSigner(delegationId(d, m), se.delegation.walletSig);
    if (!isAddressEqual(issuer, d.wallet)) {
      s.fail(`${label}: delegation`, `delegation signed by ${issuer}, not ${d.wallet}`);
      return null;
    }
    s.pass(`${label}: signature`, `signed by delegate ${who} for ${d.wallet} until ${new Date(d.validUntil * 1000).toISOString()}`);
    return id;
  } catch (e) {
    s.fail(`${label}: signature`, (e as Error).message);
    return null;
  }
}

async function checkInclusion(s: Steps, label: string, c: PublicClient, m: Manifest, room: Hex,
  inc: Inclusion | null, id: Hex, se: SignedEvent, anchorWriter: Address, createdBlock: bigint) {
  if (!inc) {
    s.fail(`${label}: inclusion`, "no inclusion proof; the event was not anchored when exported");
    return null;
  }
  let cp;
  try {
    cp = await c.readContract({ address: m.anchorLog, abi: anchorLogAbi, functionName: "checkpoint", args: [room, BigInt(inc.checkpointIndex)] });
  } catch (e) {
    s.unknown(`${label}: checkpoint`, `chain read failed: ${(e as Error).message.split("\n")[0]}`);
    return null;
  }
  if (!eq(cp.root, inc.root) || cp.count !== inc.count) {
    s.fail(`${label}: checkpoint`, `on-chain root/count ${cp.root}/${cp.count} differ from the bundle`);
    return null;
  }
  if (!isAddressEqual(cp.by, anchorWriter)) {
    s.fail(`${label}: checkpoint`, `submitted by ${cp.by}, not the room's anchor writer`);
    return null;
  }
  if (cp.blockNo < createdBlock) {
    s.fail(`${label}: checkpoint`, "checkpoint predates the room");
    return null;
  }
  s.pass(`${label}: checkpoint`, `#${inc.checkpointIndex} in block ${cp.blockNo}`);
  if (se.delegation && (cp.blockTime < BigInt(se.delegation.delegation.validFrom) || cp.blockTime > BigInt(se.delegation.delegation.validUntil))) {
    s.fail(`${label}: delegation time`, `anchored at ${cp.blockTime}, outside delegation validity window ending at ${se.delegation.delegation.validUntil}`);
    return null;
  }
  if (!verifyProof(eventLeaf(se, id, m), inc.proof, inc.root)) {
    s.fail(`${label}: Merkle path`, "leaf does not hash to the checkpoint root");
    return null;
  }
  s.pass(`${label}: Merkle path`, `leaf ${inc.leafIndex} of ${inc.count}`);
  return cp;
}

/** Verifies a proof bundle against a trusted manifest. */
export async function verifyBundle(b: ProofBundle, m: Manifest, client?: PublicClient): Promise<Report> {
  const s = new Steps();
  const c = client ?? (createPublicClient({ transport: http(m.rpc) }) as PublicClient);

  // 1. The bundle must reference the trusted deployment, not bring its own.
  if (b.chainId !== m.chainId || !isAddressEqual(b.contracts.registry, m.registry) ||
      !isAddressEqual(b.contracts.anchorLog, m.anchorLog) || !isAddressEqual(b.contracts.governance, m.governance)) {
    s.fail("deployment", "bundle references a different chain or contracts than the trusted manifest");
    return { ok: false, steps: s.list, conclusion: "Not verifiable against this deployment." };
  }
  s.pass("deployment", `chain ${m.chainId}, registry ${m.registry}`);

  // 2. Event id and signature.
  const id = await checkSigned(s, "event", b.event, m);
  if (!id) return { ok: false, steps: s.list, conclusion: "The signature is invalid." };
  if (!eq(id, b.eventId)) s.fail("event id", `recomputed ${id}, bundle says ${b.eventId}`);
  else s.pass("event id", id);

  // 3. Room and fixed membership.
  let room;
  try {
    room = await c.readContract({ address: m.registry, abi: registryAbi, functionName: "roomInfo", args: [b.room] });
  } catch (e) {
    s.unknown("room", `chain read failed: ${(e as Error).message.split("\n")[0]}`);
    return { ok: false, steps: s.list, conclusion: "Cannot decide: the chain could not be read." };
  }
  if (room.createdBlock === 0n || !eq(b.event.event.room, b.room)) {
    s.fail("room", "unknown room or event belongs to another room");
    return { ok: false, steps: s.list, conclusion: "The event does not belong to this room." };
  }
  const member = await c.readContract({ address: m.registry, abi: registryAbi, functionName: "isMember", args: [b.room, b.event.event.author] });
  if (!member) s.fail("membership", `${b.event.event.author} is not a member`);
  else s.pass("membership", `${b.event.event.author} is a fixed member since block ${room.createdBlock}`);

  // 4. Anchoring.
  const cp = await checkInclusion(s, "event", c, m, b.room, b.inclusion, id, b.event, room.anchorWriter, room.createdBlock);

  // 5. Content or authorised deletion.
  const e = b.event.event;
  let what = "";
  if (b.redaction) {
    const r = b.redaction;
    let p;
    try {
      p = await c.readContract({ address: m.governance, abi: governanceAbi, functionName: "proposal", args: [BigInt(r.proposalId)] });
    } catch (err) {
      s.unknown("deletion", `chain read failed: ${(err as Error).message.split("\n")[0]}`);
    }
    if (p) {
      if (!p.executed || !eq(p.room, b.room) || !eq(p.targetEventId, id)) s.fail("deletion", `proposal ${r.proposalId} is not an executed deletion of this event`);
      else s.pass("deletion", `proposal ${r.proposalId} executed (${p.yes} yes / ${p.no} no)`);
    }
    const t = r.tombstone.event;
    if (r.tombstone.delegation) {
      s.fail("tombstone", "tombstones must be signed by the wallet itself");
    } else if (t.kind !== KIND.tombstone || !eq(t.target, id) || !eq(t.data1, proposalIdWord(r.proposalId)) || !eq(t.room, b.room)) {
      s.fail("tombstone", "tombstone does not cite this event and proposal");
    } else {
      const tid = await checkSigned(s, "tombstone", r.tombstone, m);
      if (tid) {
        const tm = await c.readContract({ address: m.registry, abi: registryAbi, functionName: "isMember", args: [b.room, t.author] });
        if (!tm) s.fail("tombstone: membership", `${t.author} is not a member`);
        await checkInclusion(s, "tombstone", c, m, b.room, r.tombstoneInclusion, tid, r.tombstone, room.anchorWriter, room.createdBlock);
      }
    }
    if (b.content) s.fail("content", "a deleted message must not carry its body");
    what = `was later deleted under proposal ${r.proposalId}`;
  } else if (b.content) {
    if (e.kind !== KIND.message) s.fail("content", "content attached to a non-message event");
    else if (!eq(contentCommit(b.content.salt, b.content.body), e.data1)) s.fail("content", "body does not match the signed commitment");
    else s.pass("content", "body matches the signed commitment");
    what = "its supplied body matches the signed commitment";
  } else {
    s.pass("content", "body not disclosed");
    what = "its body was not disclosed";
  }

  const ok = s.ok;
  const when = cp ? `existed no later than block ${cp.blockNo} (${new Date(Number(cp.blockTime) * 1000).toISOString()})` : "is not anchored";
  const conclusion = ok
    ? `Event ${id} was signed by ${e.author}, ${when}; ${what}.`
    : `Verification did not pass: ${s.list.filter((x) => x.status !== "pass").map((x) => `${x.name} (${x.status})`).join(", ")}.`;
  return { ok, steps: s.list, conclusion };
}
