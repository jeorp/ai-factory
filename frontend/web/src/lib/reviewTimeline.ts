import type { Message, Review } from '../types';

type Entry = { kind: 'message'; key: string; message: Message } | { kind: 'review'; key: string };

export function reviewTimeline(messages: Message[], review?: Review): Entry[] {
 const entries: Entry[] = messages.map(message => ({ kind: 'message', key: message.id, message }));
 if (!review) return entries;
 let anchor = messages.findIndex(message => message.id === review.requestMessageId);
 // Older saved reviews have no request ID. Keep their result next to the saved
 // completion reply, ahead of any follow-up questions or investigations.
 if (anchor < 0 && review.head) {
  for (let i = messages.length - 1; i >= 0; i--) {
   const message = messages[i]!;
   if (message.role === 'assistant' && /^PR #\d+ のレビューを完了しました。/.test(message.text)
    && message.text.includes(review.head.slice(0, 12))) { anchor = i; break; }
  }
 }
 if (anchor < 0) anchor = messages.length - 1;
 entries.splice(anchor + 1, 0, { kind: 'review', key: `review:${review.requestMessageId || ''}:${review.base}:${review.head}` });
 return entries;
}
