import { describe, expect, it } from 'vitest';
import { errorMessage, limitationMessage } from './errorMessages';

describe('error messages', () => {
 it('distinguishes provisional no-change candidates from verified completion', () => {
  const text=errorMessage('implementation_no_change_candidates');
  expect(text).toContain('全ファイルを変更不要');expect(text).toContain('検証は未実施');
  expect(text).not.toContain('依頼を具体的に');
 });
 it('translates known codes from strings and errors', () => {
  expect(errorMessage('login_required')).toContain('ログインし直して');
  expect(errorMessage(new Error('session_busy'))).toContain('別の接続');
 });
 it('keeps the busy-run message generic and effort-specific only for effort changes', () => {
  expect(errorMessage('run_in_progress')).not.toContain('エフォート');
  expect(errorMessage('effort_change_in_progress')).toContain('エフォート');
 });
 it('falls back to a generic message that keeps the code', () => {
  expect(errorMessage('unknown_code')).toBe('処理を完了できませんでした（unknown_code）。');
 });
 it('shows a raw limitation when it is not a known code', () => {
  expect(limitationMessage('openai_http_429')).toContain('OpenAI');
  expect(limitationMessage('README.mdは大きすぎるため一部のみ確認')).toBe('README.mdは大きすぎるため一部のみ確認');
 });
});
