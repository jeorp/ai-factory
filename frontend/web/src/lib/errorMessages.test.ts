import { describe, expect, it } from 'vitest';
import { errorMessage, limitationMessage } from './errorMessages';

describe('error messages', () => {
 it('translates known codes from strings and errors', () => {
  expect(errorMessage('login_required')).toContain('ログインし直して');
  expect(errorMessage(new Error('session_busy'))).toContain('別の接続');
 });
 it('falls back to a generic message that keeps the code', () => {
  expect(errorMessage('unknown_code')).toBe('処理を完了できませんでした（unknown_code）。');
 });
 it('shows a raw limitation when it is not a known code', () => {
  expect(limitationMessage('openai_http_429')).toContain('OpenAI');
  expect(limitationMessage('README.mdは大きすぎるため一部のみ確認')).toBe('README.mdは大きすぎるため一部のみ確認');
 });
});
