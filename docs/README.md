# ドキュメント一覧

AI Factory の日本語ドキュメントの案内です。初めて動かす場合は、ルートの [README](../README.md)（英語）で固定デモを起動してから、目的に合う文書を読んでください。

## 目的別の入口

| 知りたいこと | 文書 |
| --- | --- |
| 自分のGitHubリポジトリやAIキーで動かす手順 | [開発・実接続ガイド](development.md) |
| 認証、保存、PRレビュー、実装の仕組みと上限値 | [設計と制限](architecture.md) |
| 現状のエージェントフロー、実装済み機能、改善案の優先順位 | [エージェントフローの現状と改善案](agent-workflow.md) |
| 予算配分、ドラフト保存、利用量と上限設定の改善 | [エージェントの改善内容](agent-improvements.md) |
| Cloud Runへの配置手順と公開前に残る作業 | [Cloud Run配置](deployment.md) |
| 実装の編集失敗・再試行で確認した問題と残課題 | [実装編集・照合・再試行の問題整理](implementation-edit-reliability.md) |
| プロダクトの目的、合意済みの方針、未決事項 | [MVP仕様書](product/specification.md) |
| 実装能力を評価する基盤の要件 | [実装能力評価基盤 要件書](product/ai-factory-eval-requirements.md) |

## 文書の構成

```text
docs/
├── README.md                     この一覧
├── development.md                起動・接続の手順、API / WebSocket、ログ、動作確認
├── architecture.md               設計と上限値（認証、チャット、保存、PRレビュー、実装）
├── agent-workflow.md             エージェントフロー、実装済み機能と改善案
├── agent-improvements.md         予算配分、ドラフト保存、利用量と上限設定の改善
├── deployment.md                 Cloud Run配置
├── implementation-edit-reliability.md  実装編集の失敗分析と残課題
├── product/
│   ├── specification.md          MVP仕様書（検討中）
│   └── ai-factory-eval-requirements.md  評価基盤の要件書
└── screenshots/                  READMEなどで使う画面のスクリーンショット
```

手順は development.md、仕組みと数値は architecture.md に書き分けています。同じ内容を両方に書かず、もう一方からはリンクで参照します。
