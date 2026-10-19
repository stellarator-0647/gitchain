#!/usr/bin/env node
// gitchain-verify <proof.json> <trusted-deployment.json>
import { readFileSync } from "node:fs";
import { verifyBundle, type Manifest, type ProofBundle } from "./verify.ts";

const [proofPath, manifestPath] = process.argv.slice(2);
if (!proofPath || !manifestPath) {
  console.error("usage: gitchain-verify <proof.json> <trusted-deployment.json>");
  process.exit(2);
}
const bundle = JSON.parse(readFileSync(proofPath, "utf8")) as ProofBundle;
const manifest = JSON.parse(readFileSync(manifestPath, "utf8")) as Manifest;
const report = await verifyBundle(bundle, manifest);
for (const s of report.steps) console.log(`${s.status.padEnd(7)} ${s.name}${s.detail ? ": " + s.detail : ""}`);
console.log(`\n${report.conclusion}`);
process.exitCode = report.ok ? 0 : 1; // let open HTTP handles close; process.exit() asserts on Windows
