# 実装編集の検証と再試行

実装エージェントの編集案は計画と応答構造を検証してから原文へ適用する。拒否理由を次のモデル入力に渡し、各段階の呼び出し予算内で訂正を求める。

## 適用前の構造検証

[validateProposalStructure](../backend/internal/agent/implement_changes.go) は、工程外のパス、同一パスの重複、計画と違う操作、操作ごとの欄の誤りを全項目から集める。1件でもあれば原文への適用と保存を拒否する。

編集スキーマは工程内のパスと計画上の `action` を対応させる。プロバイダーが受け付けるスキーマに変換しても、サーバー側の検証は維持する。誤った `create` を自動で `update` に置き換えず、正しい操作で再生成させる。

構造の拒否は行範囲方式への切替理由にしない。有効な編集が同じ応答にあっても、不正な項目を含む応答は保存しない。訂正では有効な変更も保持して出し直すよう求める。

## 原文との照合

検索・置換は検索文字列が現在の原文で1か所に一致する場合だけ適用する。不一致や複数一致は `line_ranges` に切り替え、固定原文のSHA-256、開始・終了行、両端の照合文字列、範囲の重複を検証する。原文の範囲外のバイト列とCRLFを保持する。

`implementTask.editFailure` はサーバーが固定原文から作る。対象パス、ハッシュ、行数、正しい境界行を渡し、モデルが返した誤った原文を正解として再利用しない。空行、範囲外、4,096バイトを超える行、境界に使えない改行文字を含む行は照合文字列として返さない。

操作の誤りには `changeIssues` で提出位置、計画上のパス、期待操作、提出操作を返す。工程外のパスなど、モデルが書いた自由文をそのまま訂正指示へ再掲しない。追加取得を挟んでも訂正情報を保持する。

## 再試行の範囲

編集の1区切りは5呼び出しで、取得は最大2回、変更生成と訂正に3回分を確保する。形式不正は連続3回の失敗まで訂正し、一時的な通信エラーは最大2回再試行する。Geminiの時間切れは分割できる編集を優先し、それ以外は最大1回再試行する。

再試行前に状態を保存し、実装全体の100呼び出し・30分の上限とセッション累計のトークン上限を守る。停止・認証失敗・保存失敗・期限切れは再試行しない。GitHubへの書き込みは保存済みの公開状態から再開する。

## ログとテスト

通常ログ `implement_change_rejected` は固定コード、位置、操作を記録し、原文は含めない。ローカル限定の詳細ログでは原文ハッシュ、期待・実際の境界行、周辺コードを確認できる。収集と復元は[開発ガイド](development.md#ローカル限定の詳細debugログ)を参照する。

| 確認する内容 | 実装・テスト |
| --- | --- |
| パスと操作の対応、適用前の構造検証 | [implement_schema.go](../backend/internal/agent/implement_schema.go)、[implement_structure_test.go](../backend/internal/agent/implement_structure_test.go) |
| 訂正情報と原文の照合 | [implement_edit_recovery.go](../backend/internal/agent/implement_edit_recovery.go)、[implement_edit_recovery_test.go](../backend/internal/agent/implement_edit_recovery_test.go) |
| 境界行などの入力上限 | [implement_edit_limits_test.go](../backend/internal/agent/implement_edit_limits_test.go) |
| 通信エラーの再試行 | [implement_retry.go](../backend/internal/agent/implement_retry.go)、[implement_recovery_test.go](../backend/internal/agent/implement_recovery_test.go) |
| 編集と追加取得の予算配分 | [implement_budget_test.go](../backend/internal/agent/implement_budget_test.go) |

これらは拒否・訂正処理のテストであり、実モデルによる編集成功率の測定結果とは区別する。
