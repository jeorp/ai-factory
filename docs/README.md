# ドキュメント一覧

固定デモの起動はルートの [README](../README.md#run-locally) を参照する。以下の文書は現在の実装の利用・開発に必要な情報をまとめる。

| 知りたいこと | 文書 |
| --- | --- |
| 利用できる機能と操作の流れ | [MVP仕様書](product/specification.md) |
| 実接続、プロバイダー設定、API、動作確認、ログ | [開発・実接続](development.md) |
| 認証、保存、承認条件、実行上限、コードの参照先 | [設計と制限](architecture.md) |
| エージェントの段階と状態遷移 | [エージェントフロー](agent-workflow.md) |
| 編集の照合、拒否理由、訂正、関連テスト | [実装編集の検証と再試行](implementation-edit-reliability.md) |
| コンテナ、認証、Secret Manager、Firestoreの配置設定 | [Cloud Run配置](deployment.md) |

手順とAPIは `development.md`、処理の制約と保存先は `architecture.md` にまとめる。コードを変更するときは、設計文書にある参照先と関連テストを確認する。
