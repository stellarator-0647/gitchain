import { test } from 'node:test';
import assert from 'node:assert/strict';
import { proposalState } from './proposal-state.ts';
import { explainError } from './labels.ts';
import type { Proposal } from './types.ts';

const T = ('0x' + 'aa'.repeat(32)) as `0x${string}`;
const p = (id: number, yes: number, no: number, executed = false, target = T) =>
  ({ id, room: T, target, proposer: '0x1', createdBlock: 1, deadlineBlock: 100, yes, no, executed }) as Proposal;

test('open proposal says how many yes votes are missing', () => {
  const s = proposalState(p(1, 1, 0), [p(1, 1, 0)], 3);
  assert.equal(s.label, '投票中');
  assert.ok(s.canVote && !s.canExecute);
  assert.match(s.note!, /还需 1 票/);
});

test('majority makes it executable and closes voting', () => {
  const s = proposalState(p(1, 2, 0), [p(1, 2, 0)], 3);
  assert.equal(s.label, '可执行');
  assert.ok(s.canExecute && !s.canVote);
});

test('a proposal that can no longer pass is rejected, not executable', () => {
  const s = proposalState(p(1, 0, 3), [p(1, 0, 3)], 3);
  assert.equal(s.label, '已否决');
  assert.ok(!s.canExecute && !s.canVote);
  assert.equal(proposalState(p(1, 0, 2), [p(1, 0, 2)], 3).label, '已否决');
});

test('a second proposal on an already deleted target is void', () => {
  const all = [p(1, 0, 0), p(2, 2, 0, true)];
  const s = proposalState(all[0], all, 3);
  assert.equal(s.label, '已失效');
  assert.match(s.note!, /提案 2/);
  assert.ok(!s.canExecute && !s.canVote);
});

test('contract reverts are explained in Chinese', () => {
  assert.match(explainError('execute would revert: execution reverted: custom error 0x0b7b8dc6'), /赞成票未过半/);
  assert.match(explainError('The contract function "execute" reverted. Error: AlreadyRedacted()'), /已被另一个提案删除/);
  assert.equal(explainError('something else'), 'something else');
});
