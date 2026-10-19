# GitChain — notes for AI coding assistants

Read `README.md`, `CONTRIBUTING.md` and `docs/protocol.md` first; `docs/client.md` covers the Windows desktop shell and `docs/history/` the dated change and validation records.

- Never reset a running devnet (`infra/scripts/devnet.sh reset`/`up`) just to look at the UI; use `restart-nodes`.
- Never commit private keys, node API tokens, `.devnet/`, `deployments/<chainId>.json` or personal data. The Anvil keys in scripts and tests are public test keys only.
- A passing test only covers what it states. The desktop smoke test does not prove wallet signing or three-node sync.
- Protocol, signature or proof-format changes must update Go, `verifier/` and `testdata/vectors.json` together.
