// verifyBundle end to end against a stubbed chain: format checks run before
// any chain read, and a failed membership read is "unknown", never a throw.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import type { PublicClient } from "viem";
import { privateKeyToAccount } from "viem/accounts";
import { EVENT_TYPES, domainOf, eventId, leaf, proposalIdWord, verifyBundle, type Manifest, type ProofBundle } from "../src/verify.ts";

const v = JSON.parse(readFileSync(new URL("../../testdata/vectors.json", import.meta.url), "utf8"));
const e0 = v.events[0];
const manifest: Manifest = {
  chainId: v.domain.chainId,
  rpc: "http://127.0.0.1:1",
  registry: v.domain.verifyingContract,
  anchorLog: "0x00000000000000000000000000000000000000a1",
  governance: "0x00000000000000000000000000000000000000a2",
};
const writer = "0x00000000000000000000000000000000000000b1";

// A single-leaf checkpoint: the root is the event's own leaf.
function bundle(): ProofBundle {
  return structuredClone({
    version: 2,
    chainId: manifest.chainId,
    room: v.room,
    contracts: { registry: manifest.registry, anchorLog: manifest.anchorLog, governance: manifest.governance },
    event: e0.signed,
    eventId: e0.eventId,
    content: e0.blob,
    inclusion: { checkpointIndex: 0, root: e0.leaf, count: 1, leafIndex: 0, proof: [] },
    redaction: null,
  });
}

type Reads = Partial<Record<"roomInfo" | "isMember" | "checkpoint" | "proposal", (args: unknown[]) => unknown>>;
function chain(over: Reads = {}): PublicClient & { calls: number } {
  const base: Required<Reads> = {
    roomInfo: () => ({ creator: writer, anchorWriter: writer, createdBlock: 1n, memberCount: 3 }),
    isMember: () => true,
    proposal: () => { throw new Error("no proposal"); },
    checkpoint: () => ({ gitCommit: "0x" + "00".repeat(32), root: e0.leaf, count: 1, by: writer, blockNo: 5n, blockTime: 1760000100n, prevIndex: 2n ** 256n - 1n }),
  };
  const c = {
    calls: 0,
    async readContract({ functionName, args }: { functionName: keyof Reads; args: unknown[] }) {
      c.calls++;
      return (over[functionName] ?? base[functionName])(args);
    },
  };
  return c as unknown as PublicClient & { calls: number };
}

test("a well-formed anchored message passes", async () => {
  const r = await verifyBundle(bundle(), manifest, chain());
  assert.equal(r.ok, true, JSON.stringify(r.steps));
  assert.equal(r.facts.author?.toLowerCase(), e0.signed.event.author.toLowerCase());
  assert.equal(r.facts.checkpointIndex, 0);
  assert.equal(r.facts.blockNo, 5);
  assert.equal(r.facts.content, "matched");
});

test("membership read failure is unknown, not a throw", async () => {
  const r = await verifyBundle(bundle(), manifest, chain({ isMember: () => { throw new Error("rpc down"); } }));
  assert.equal(r.ok, false);
  assert.equal(r.steps.find((s) => s.name === "membership")?.status, "unknown");
});

test("non-member author fails", async () => {
  const r = await verifyBundle(bundle(), manifest, chain({ isMember: () => false }));
  assert.equal(r.steps.find((s) => s.name === "membership")?.status, "fail");
});

test("unsupported version is rejected before any chain read", async () => {
  const c = chain();
  const b = { ...bundle(), version: 3 };
  const r = await verifyBundle(b, manifest, c);
  assert.equal(r.ok, false);
  assert.equal(r.steps[0].name, "format");
  assert.equal(c.calls, 0);
});

for (const [name, mutate] of [
  ["missing version", (b: any) => delete b.version],
  ["short event id", (b: any) => (b.eventId = "0x1234")],
  ["fractional seq", (b: any) => (b.event.event.seq = 1.5)],
  ["string ts", (b: any) => (b.event.event.ts = "1760000000")],
  ["kind out of range", (b: any) => (b.event.event.kind = 256)],
  ["bad author", (b: any) => (b.event.event.author = "0x12")],
  ["proof not hashes", (b: any) => (b.inclusion.proof = ["0xzz"])],
  ["content without salt", (b: any) => (b.content = { body: "x" })],
  ["redaction without tombstone", (b: any) => (b.redaction = { proposalId: 1, tombstone: null, tombstoneInclusion: null })],
] as const) {
  test(`malformed bundle: ${name}`, async () => {
    const b = bundle();
    mutate(b);
    const c = chain();
    const r = await verifyBundle(b, manifest, c);
    assert.equal(r.ok, false);
    assert.equal(r.steps[0].name, "format");
    assert.equal(c.calls, 0);
  });
}

test("tombstone membership read failure is unknown", async () => {
  // Anvil account 0 is the public test key that signed the vectors.
  const acct = privateKeyToAccount("0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80");
  const t = { ...e0.signed.event, seq: 1, authorPrev: e0.eventId, kind: 3, target: e0.eventId, data1: proposalIdWord(1) };
  const tid = eventId(t, manifest);
  const sig = await acct.signTypedData({ domain: domainOf(manifest), types: EVENT_TYPES, primaryType: "Event", message: { ...t, seq: 1n, ts: BigInt(t.ts) } });
  const tleaf = leaf(tid, sig);
  const b = { ...bundle(), content: null,
    inclusion: { checkpointIndex: 0, root: e0.leaf, count: 1, leafIndex: 0, proof: [] },
    redaction: { proposalId: 1, tombstone: { event: t, sig }, tombstoneInclusion: { checkpointIndex: 1, root: tleaf, count: 1, leafIndex: 0, proof: [] } } };
  let n = 0;
  const c = chain({
    isMember: () => { if (n++ === 0) return true; throw new Error("rpc down"); },
    checkpoint: ((args: unknown[]) => ({ gitCommit: "0x" + "00".repeat(32), root: args[1] === 0n ? e0.leaf : tleaf, count: 1, by: writer, blockNo: 5n, blockTime: 1760000100n, prevIndex: 0n })) as never,
    proposal: () => ({ room: v.room, targetEventId: e0.eventId, reasonHash: "0x" + "00".repeat(32), proposer: writer, createdBlock: 2n, deadlineBlock: 9n, yes: 2, no: 0, executed: true }),
  });
  const r = await verifyBundle(b as ProofBundle, manifest, c);
  assert.equal(r.ok, false, JSON.stringify(r.steps));
  assert.equal(r.steps.find((s) => s.name === "tombstone: membership")?.status, "unknown");
  assert.equal(r.steps.find((s) => s.name === "deletion")?.status, "pass");
  assert.equal(r.steps.find((s) => s.name === "tombstone: Merkle path")?.status, "pass");
  assert.equal(r.facts.content, "deleted");
  assert.equal(r.facts.proposalId, 1);
});
