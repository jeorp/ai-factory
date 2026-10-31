import { describe, expect, it } from 'vitest';
import { reviewTimeline } from './reviewTimeline';
import type { Message, Review } from '../types';

const message = (id: string, role: Message['role'] = 'user', text = id): Message => ({ id, role, text, at: '', applied: true });
const review = (requestMessageId?: string): Review => ({ requestMessageId, base: 'b'.repeat(40), head: 'a'.repeat(40) }) as Review;
const keys = (messages: Message[], value?: Review) => reviewTimeline(messages, value).map(entry => entry.kind === 'review' ? 'review' : entry.message.id);

describe('review placement in the conversation', () => {
 it('shows the result after its request and before later replies and questions', () => {
  const messages = [message('earlier'), message('request'), message('complete', 'assistant'), message('follow-up')];
  expect(keys(messages, review('request'))).toEqual(['earlier', 'request', 'review', 'complete', 'follow-up']);
  expect(keys([...messages, message('investigation', 'assistant')], review('request')))
   .toEqual(['earlier', 'request', 'review', 'complete', 'follow-up', 'investigation']);
 });
 it('places a new review at its own request even for the same commits', () => {
  expect(keys([message('first'), message('second')], review('second'))).toEqual(['first', 'second', 'review']);
 });
 it('keeps older saved reviews with their completion reply across reloads and follow-ups', () => {
  const messages = [message('request'), message('complete', 'assistant', `PR #42 のレビューを完了しました。対象ヘッド: ${'a'.repeat(12)}。`), message('follow-up')];
  expect(keys(messages, review())).toEqual(['request', 'complete', 'review', 'follow-up']);
  expect(keys(messages, review('missing'))).toEqual(['request', 'complete', 'review', 'follow-up']);
  const repeated = [...messages, message('repeat-complete', 'assistant', messages[1]!.text), message('later')];
  expect(keys(repeated, review())).toEqual(['request', 'complete', 'follow-up', 'repeat-complete', 'review', 'later']);
 });
 it('falls back to the end when no saved anchor exists', () => {
  const messages = [message('other', 'assistant', `PR #42 のレビューを完了しました。対象ヘッド: ${'c'.repeat(12)}。`), message('request')];
  expect(keys(messages, review())).toEqual(['other', 'request', 'review']);
  expect(keys([], review())).toEqual(['review']);
 });
 it('preserves a normal conversation without a review', () => {
  expect(keys([message('question'), message('answer', 'assistant')])).toEqual(['question', 'answer']);
  expect(keys([])).toEqual([]);
 });
});
