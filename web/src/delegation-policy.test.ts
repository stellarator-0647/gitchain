import { test } from 'node:test';
import assert from 'node:assert/strict';
import { delegationId, DELEGATION_TYPES } from '@gitchain/verifier';
import { validateDelegationDraft } from './delegation-policy.ts';
import type { Status, DelegationDraft } from './types.ts';
const wallet='0x1111111111111111111111111111111111111111', delegate='0x2222222222222222222222222222222222222222', registry='0x3333333333333333333333333333333333333333';
const room=('0x'+'aa'.repeat(32)) as `0x${string}`;
const st={wallet,room,chainId:31337,contracts:{registry}} as Status;
function draft():DelegationDraft {
 const delegation={room,wallet,delegate,validFrom:1000,validUntil:4600,kinds:6} as const;
 const id=delegationId(delegation,{chainId:31337,registry});
 return {delegation,id,typedData:{primaryType:'Delegation',domain:{name:'GitChain',version:'2',chainId:'31337',verifyingContract:registry},types:DELEGATION_TYPES as never,message:Object.fromEntries(Object.entries(delegation).map(([k,v])=>[k,String(v)]))}};
}
test('accept canonical one-hour authorization',()=>assert.doesNotThrow(()=>validateDelegationDraft(draft(),st,delegate,3600,1000)));
test('reject payload switched by node',()=>{const d=draft();d.typedData.message.delegate=wallet;assert.throws(()=>validateDelegationDraft(d,st,delegate,3600,1000));});
test('reject excessive lifetime, privileges and wrong deployment',()=>{
 for (const mutate of [(d:DelegationDraft)=>{d.delegation.validUntil=8200;},(d:DelegationDraft)=>{d.delegation.kinds=14;},(d:DelegationDraft)=>{d.typedData.domain.verifyingContract=wallet;}]) {const d=draft();mutate(d);assert.throws(()=>validateDelegationDraft(d,st,delegate,3600,1000));}
});
