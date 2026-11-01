# JavaScript / TypeScript

- JS-ABSENCE: API・保存・既定値では、未指定、undefined、null、空文字、0、falseの契約を区別する。||は有効なfalsy値も置き換える。??もnullを欠損として扱ってよい場合に限る。型アサーションや非nullアサーションは実行時の入力検証ではない。
- JS-ALIAS-STATE: オブジェクトや配列のspreadは浅いコピーで、ネストした参照は共有される。失敗時の保持・入力非変更が要求される場合、throw/reject以前の初期化や更新、共有参照経由の副作用を全フィールドについて調べる。
- JS-ASYNC: await/returnされないPromiseやcatchによる失敗の握りつぶしを調べる。Promise.allのrejectで他の処理が自動的に取り消されると扱わない。途中までの状態変更・外部操作、再試行による二重実行が契約に反しないか確認する。
- JS-JSON: JSON.stringifyではobjectのundefined値は通常省略され、array内ではnullになる。送信から受け取り・保存・再読込まで、欠損と明示的なnullが契約どおり扱われるか確認する。

根拠:
- https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Operators/Nullish_coalescing
- https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Operators/Spread_syntax
- https://developer.mozilla.org/en-US/docs/Web/JavaScript/Guide/Using_promises
- https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/JSON/stringify
- https://www.typescriptlang.org/docs/handbook/2/everyday-types.html#type-assertions
