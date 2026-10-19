import { hashTypedData, isAddressEqual } from 'viem';
import { delegationId } from '@gitchain/verifier';
import type { Address } from 'viem';
import type { DelegationDraft, Status } from './types.ts';
/** Check both representations before asking a wallet to authorize a key. */
export function validateDelegationDraft(d: DelegationDraft, st: Status, delegate: Address, seconds: number, now = Date.now()/1000) {
  const a = d.delegation, td = d.typedData;
  if (td.primaryType !== 'Delegation' || td.domain.name !== 'GitChain' || td.domain.version !== '2' ||
      Number(td.domain.chainId) !== st.chainId || !isAddressEqual(td.domain.verifyingContract as Address, st.contracts.registry) ||
      !isAddressEqual(a.wallet, st.wallet) || !isAddressEqual(a.delegate, delegate) || a.room.toLowerCase() !== st.room.toLowerCase() ||
      a.kinds !== 6 || !Number.isSafeInteger(a.validFrom) || !Number.isSafeInteger(a.validUntil) ||
      a.validFrom > now + 30 || a.validUntil <= now || a.validUntil - a.validFrom <= 0 ||
      a.validUntil - a.validFrom > Math.min(seconds, 7200)) throw new Error('授权的部署、权限或有效期与本次请求不一致');
  const expected = delegationId(a, {chainId:st.chainId,registry:st.contracts.registry});
  const actual = hashTypedData({domain:{...td.domain,chainId:st.chainId,verifyingContract:st.contracts.registry},types:td.types,primaryType:td.primaryType,message:td.message} as never);
  if (expected.toLowerCase() !== d.id.toLowerCase() || actual.toLowerCase() !== expected.toLowerCase()) throw new Error('授权字段与钱包签名载荷不一致');
}
