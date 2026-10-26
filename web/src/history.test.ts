import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mergePages } from './history.ts';
import type { Message } from './types.ts';

const m = (id: string) => ({ eventId: id }) as Message;

test('older pages come first and keep their order', () => {
  assert.deepEqual(mergePages([[m('0x01'), m('0x02')], [m('0x03')]]).map((x) => x.eventId), ['0x01', '0x02', '0x03']);
});

test('an event that slid across a page boundary appears once', () => {
  // A new message arrived between the two page reads, pushing 0x02 into page 1 as well.
  assert.deepEqual(mergePages([[m('0x01'), m('0x02')], [m('0x02'), m('0x03'), m('0x04')]]).map((x) => x.eventId), ['0x01', '0x02', '0x03', '0x04']);
});

test('event ids are compared case-insensitively', () => {
  assert.equal(mergePages([[m('0xAB')], [m('0xab')]]).length, 1);
});
