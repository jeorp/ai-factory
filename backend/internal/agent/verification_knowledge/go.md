# Go

- GO-ERROR-STATE: 失敗時に変更しない契約がある場合、Balances/Seenのような全フィールドについて、検証・残高確認・重複確認より前の代入、make、map更新を調べる。nilを空map/sliceへ変えることも状態変更。初期化が常に不正なのではなく、契約に従う。deferによる変更もerror return時に確認する。
- GO-NIL: nil mapの参照は要素型のゼロ値を返せるが、要素への代入はpanicする。nil/空の区別がAPIや保存の契約に関わるときは、encoding/jsonのタグ・カスタム処理も確認する。通常のnil sliceはnullになり、空sliceと異なる。
- GO-ALIAS: mapの代入やslice・structの浅いコピーだけで独立した状態になったと扱わない。sliceの要素更新やappendが共有配列へ及ぼす影響、ネストした参照を追い、失敗時の元状態保持や入力非変更の契約と照合する。
- GO-CONCURRENCY: 同一状態の読み取り・検証・書き込みと重複ID確認を、必要な同期範囲全体で確認する。IDの有効範囲が台帳単位なら、グローバル共有や過去IDの忘却を調べる。mutexの存在だけで原子性を確認したとしない。

根拠:
- https://go.dev/ref/spec#Map_types
- https://go.dev/ref/spec#Index_expressions
- https://go.dev/ref/spec#Slice_types
- https://go.dev/ref/spec#Defer_statements
- https://pkg.go.dev/encoding/json#Marshal
- https://go.dev/doc/articles/race_detector
