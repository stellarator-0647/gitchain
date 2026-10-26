// Browser wallet (EIP-1193, e.g. MetaMask). Messages are signed with
// eth_signTypedData_v4 over the node's typed data; governance actions are
// plain contract transactions sent by the wallet itself, never by the node.
import { createPublicClient, createWalletClient, custom, defineChain, getAddress, type Address, type EIP1193Provider, type Hex } from "viem";
import type { Status, TypedData } from "./types";

declare global {
  interface Window {
    ethereum?: EIP1193Provider;
    gitchainDesktop?: { openWalletBrowser: () => Promise<void> };
  }
}

export const hasWallet = () => typeof window !== "undefined" && !!window.ethereum;

export async function connectWallet(): Promise<Address> {
  if (!window.ethereum) throw new Error("没有检测到浏览器钱包");
  const [acct] = (await window.ethereum.request({ method: "eth_requestAccounts" })) as Address[];
  return getAddress(acct);
}

/** Asks the wallet to switch to chainId, adding it from the node's RPC if unknown. */
async function ensureChain(chainId: number, rpc?: string) {
  const eth = window.ethereum!;
  const current = parseInt((await eth.request({ method: "eth_chainId" })) as string, 16);
  if (current === chainId) return;
  const hex = `0x${chainId.toString(16)}`;
  try {
    await eth.request({ method: "wallet_switchEthereumChain", params: [{ chainId: hex }] } as never);
  } catch (e) {
    const code = (e as { code?: number }).code;
    if (code !== 4902 || !rpc) throw new Error(`钱包当前在链 ${current}，请切换到链 ${chainId}`);
    await eth.request({
      method: "wallet_addEthereumChain",
      params: [{ chainId: hex, chainName: `GitChain ${chainId}`, rpcUrls: [rpc], nativeCurrency: { name: "ETH", symbol: "ETH", decimals: 18 } }],
    } as never);
  }
  const now = parseInt((await eth.request({ method: "eth_chainId" })) as string, 16);
  if (now !== chainId) throw new Error(`钱包当前在链 ${now}，请切换到链 ${chainId}`);
}

/** Signs the node-prepared typed data after checking chain and account. */
export const signDraft = (td: TypedData, expected: Address, chainId: number, rpc?: string) =>
  signTypedDataWithWallet(td, expected, chainId, rpc);

/** Signs any typed data (event or delegation) with the wallet. */
export async function signTypedDataWithWallet(td: TypedData, expected: Address, chainId: number, rpc?: string): Promise<Hex> {
  const acct = await connectWallet();
  if (acct.toLowerCase() !== expected.toLowerCase()) throw new Error(`钱包账户 ${acct} 与节点绑定的成员 ${expected} 不一致`);
  await ensureChain(chainId, rpc);
  if (Number(td.domain.chainId) !== chainId) throw new Error("签名数据的 chainId 与节点配置不一致，拒绝签名");
  return (await window.ethereum!.request({
    method: "eth_signTypedData_v4",
    params: [acct, JSON.stringify(td)],
  } as never)) as Hex;
}

const governanceAbi = [
  { type: "function", name: "proposeRedaction", stateMutability: "nonpayable",
    inputs: [{ name: "room", type: "bytes32" }, { name: "targetEventId", type: "bytes32" }, { name: "reasonHash", type: "bytes32" }],
    outputs: [{ type: "uint256" }] },
  { type: "function", name: "vote", stateMutability: "nonpayable",
    inputs: [{ name: "id", type: "uint256" }, { name: "support", type: "bool" }], outputs: [] },
  { type: "function", name: "execute", stateMutability: "nonpayable", inputs: [{ name: "id", type: "uint256" }], outputs: [] },
  { type: "function", name: "proposeOrder", stateMutability: "nonpayable",
    inputs: [{ name: "room", type: "bytes32" }, { name: "a", type: "bytes32" }, { name: "b", type: "bytes32" }],
    outputs: [{ type: "uint256" }] },
  { type: "function", name: "voteOrder", stateMutability: "nonpayable",
    inputs: [{ name: "id", type: "uint256" }, { name: "aFirst", type: "bool" }], outputs: [] },
] as const;

async function clients(st: Status) {
  await ensureChain(st.chainId, st.rpc);
  const chain = defineChain({ id: st.chainId, name: `chain-${st.chainId}`, nativeCurrency: { name: "ETH", symbol: "ETH", decimals: 18 }, rpcUrls: { default: { http: [] } } });
  const transport = custom(window.ethereum!);
  const account = await connectWallet();
  return { wallet: createWalletClient({ chain, transport, account }), pub: createPublicClient({ chain, transport }) };
}

/** Sends a governance transaction from the wallet and waits for it. */
export async function governance(st: Status, fn: "proposeRedaction" | "vote" | "execute" | "proposeOrder" | "voteOrder", args: readonly unknown[]) {
  const { wallet, pub } = await clients(st);
  const hash = await wallet.writeContract({ address: st.contracts.governance, abi: governanceAbi, functionName: fn, args: args as never });
  const rc = await pub.waitForTransactionReceipt({ hash });
  if (rc.status !== "success") throw new Error(`交易 ${hash} 失败`);
  return hash;
}
