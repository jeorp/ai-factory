# 開発・実接続

ローカルの固定デモを起動する手順は [README](../README.md#run-locally) を参照する。ここでは実サービスへの接続、API、動作確認をまとめる。

## 接続モード

GitHubとAIの接続先は別々に設定する。ルートの `.env.example` を `.env` にコピーし、利用する組み合わせを指定する。

| GitHub | `PROVIDER` | `AI_PROVIDER` |
| --- | --- | --- |
| 固定デモ・固定AI | `demo` | `demo` |
| 固定デモ・実AI | `demo` | `openai`、`gemini`、`anthropic`、`vertex` から選択 |
| 実リポジトリ・実AI | `live` | `openai`、`gemini`、`anthropic`、`vertex` から選択 |

複数の実AIは `AI_PROVIDER=gemini,anthropic,vertex` のようにカンマ区切りで併用できる。モデルごとにプロバイダーを振り分け、画面の資格情報入力欄も切り替える。
`.env.example` の `AI_PROVIDER=demo` も接続先に合わせて変更する。実GitHubとデモAIの組み合わせは起動時に拒否される。変更後はリポジトリルートでAPIを再ビルドする。

```sh
docker compose up --build -d --wait
```

### GitHub App

1. GitHub Appを作成する。Repository permissionsはMetadata・IssuesをRead-only、Contents・Pull requestsをRead and write、Checks・ActionsをRead-onlyにする。読み取りだけで使う場合はContents・Pull requestsもRead-onlyにできる。その場合、書き込みは `repository_write_forbidden` で拒否される。Workflowsの権限は付けない。
2. User access tokenの有効期限を有効にし、Callback URLを `http://127.0.0.1:5175/auth/callback` にする。
3. 利用するリポジトリへAppをインストールする。Appと本人の両方がアクセスできる範囲を参照する。
4. `.env` に `PROVIDER=live`、AI接続先、`GITHUB_CLIENT_ID`、`GITHUB_CLIENT_SECRET`、`GITHUB_APP_SLUG` を設定する。GitHub Appの認証情報を使う。サーバーは `ghu_` user access tokenを受け付ける。
5. ComposeとWebを起動し、GitHubでログインする。セッションを作成し、画面の送信先・費用の説明を確認して、選んだAIのAPIキーを入力する。

Checks・ActionsはPRのCI結果と失敗ログに使う。権限追加後の403ではインストール先の権限更新を承認し、GitHubでログインし直す。

### AI設定

| 環境変数 | 内容 |
| --- | --- |
| `OPENAI_MODELS` | カンマ区切りのモデル許可リスト。既定は `gpt-5-mini` |
| `GEMINI_MODELS` | Geminiのモデル許可リスト。未設定時の一覧は [DefaultGeminiModels](../backend/internal/httpapi/config.go) を参照 |
| `ANTHROPIC_MODELS` | Anthropic APIで使うClaudeの許可リスト |
| `CLAUDE_MODELS` | Vertex AIで使うClaudeの許可リスト |
| `VERTEX_PROJECT_ID` / `VERTEX_REGION` | Vertex AIのプロジェクトとリージョン。リージョンの既定は `global` |
| `GEMINI_REQUEST_TIMEOUT` | Geminiの1リクエストの待ち時間。既定 `180s`、許容範囲 `1s`〜`10m` |

許可リストの先頭が画面の既定モデルになる。OpenAI接続はResponses APIの構造化出力と `reasoning.effort=minimal` を使う。Gemini接続はDeveloper APIを使い、Gemini 3以降ではセッションの推論量を `thinkingLevel` に送る。新規セッションと停止中の会話から変更でき、対応値と既定値は [effort.go](../backend/internal/domain/effort.go) に従う。モデルを変更する場合は、このリクエスト形式との互換性を確認する。

Anthropic APIはAPIキーを画面で入力する。Vertex AIは指定プロジェクトで利用するClaudeを有効にし、利用者が取得したGoogle OAuthアクセストークンを画面で入力する。サーバーは利用者のGoogle認証情報から自動取得しない。Vertex AIのモデルIDは `vertex/` を付け、Anthropic APIの同名モデルと区別する。

APIキーやアクセストークンは画面で入力し、`.env` や会話には書かない。画面で保存を選ぶとIndexedDBに残るため、削除には保存済みキーの削除操作を使う。実行用キーの1時間の期限と、ブラウザーへ保存したコピーは別に管理する。

利用可能なモデル、料金、データの取り扱いは、利用するプロバイダーの条件を確認する。画面の料金は [pricing.ts](../frontend/web/src/lib/pricing.ts) の登録単価からの推定であり、プロバイダーへの請求額とは区別する。

## Go APIを直接起動

先にDocker版APIを停止してポートの競合を避ける。以下は固定デモでの起動例。

```sh
docker compose stop api
docker compose up -d --wait firebase-emulator
cd backend
APP_ENV=local PROVIDER=demo GCP_PROJECT_ID=demo-ai-factory WEB_ORIGIN=http://127.0.0.1:5175 PORT=8181 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9197 FIRESTORE_EMULATOR_HOST=127.0.0.1:8186 go run ./cmd/api
```

`go run` はルートの `.env` を自動で読み込まない。実接続では必要な変数をプロセスの環境へ渡す。ローカル設定は `demo-` プロジェクト、Auth・Firestoreの両Emulator、loopbackのHTTP Originを必須とする。本番設定はEmulatorと固定デモを拒否する。

Composeのホスト側ポートはAuth `9197`、Firestore `8186`。`firebase.json` の `9099`・`8085` はコンテナ内のポートである。

## HTTP API

エラーは `{ "error": "code" }` で返す。`/api/v1/` はFirebase ID tokenのBearerとGitHub用HttpOnly CookieのUID一致を必須とする。書き込みはOriginも検証する。

| Method | Path | 内容 |
| --- | --- | --- |
| GET | `/health/live` | プロセスの稼働確認 |
| GET | `/api/config` | デモ状態、AI接続先、モデル、App設定URL、モデルごとのプロバイダー・推論量、レビュー上限 |
| GET | `/auth/github` | state・PKCE付きOAuth開始 |
| GET | `/auth/callback` | コード交換、暗号化Cookie、リダイレクト |
| POST | `/auth/complete` | 同一Origin限定でFirebaseカスタムトークン取得 |
| POST | `/auth/logout` | GitHub Cookie破棄 |
| GET | `/api/v1/me` | ログイン名 |
| GET | `/api/v1/repositories` | Appと本人がアクセスできるリポジトリ |
| GET / POST | `/api/v1/sessions` | 直近40セッションの一覧／作成 |
| GET / DELETE | `/api/v1/sessions/{id}` | 本人のセッション取得／削除 |
| GET | `/api/v1/sessions/{id}/file?path=...&commit=...` | 記録したコミットのソースファイル |
| GET | `/api/v1/sessions/{id}/change?path=...` | 変更前後のファイル本文 |
| POST | `/api/v1/sessions/{id}/changes/extend` | 生成内容の期限を延長。本文不要、成功時は `expiresAt` を返す。期限切れは410 |
| GET | `/api/v1/sessions/{id}/review-diff?index=...` | レビュー計画の0始まりのファイル番号に対応する差分 |
| GET | `/api/v1/sessions/{id}/ws` | WebSocketアップグレード |

## WebSocket

接続から10秒以内に最初のフレームを送る。

```json
{"type":"auth","token":"Firebase ID token"}
```

認証とlease取得後に `session` が返る。認証情報をURLに入れない。各コマンドは `type` と必要なフィールドを送る。AIを使うコマンドには `apiKey` と、ミリ秒Unix時刻の `keyExpiresAt` を付ける。

| `type` | 主なフィールド・動作 |
| --- | --- |
| `message` | `id`, `text`。通常の質問。意図に応じてレビュー・実装へ移る |
| `set_effort` | `effort`。停止中の会話の推論量を変更 |
| `set_budget` | `tokenBudget`。停止中に累計トークン上限を保存。0は無制限、最大100,000,000 |
| `instruction` | `id`, `text`。追加指示を保存し、AI呼び出しの間で反映 |
| `review` | `id`, `text`。PRを対象にしたセッションでレビューを開始 |
| `investigate` | `id`, `unverifiedIndex`。レビューの未確認事項を0始まりの番号で追加調査 |
| `implement` | `id`, `text`, 任意の `pullNumber`。実装計画、または既存PRの修正計画を作る |
| `implement_approve` | 計画を承認して編集 |
| `implement_amend` | `text` に `approve` / `decline`。追加ファイルを承認／見送り |
| `implement_verify` | `verification_blocked` の変更を再調査 |
| `implement_publish` | `branch`, `title`, `body`。確認した変更を書き込む。AIキー不要 |
| `implement_check_ci` | 公開したコミットのCIを再確認。AIキー不要 |
| `implement_fix_ci` | 失敗したCIから修正計画を作る |
| `resume` | 現在のワークフローを保存位置から再開。書き込み・CI確認だけの再開はAIキー不要 |
| `stop` | キャンセル要求。`stopping` から `stopped` へ移る |
| `end` | 実行を止めて接続を終了 |

メッセージIDは英数字・`_`・`-` の16〜80文字で、保存済みIDの再送は再実行しない。入力本文と追加指示は1件4,096バイトまで。

受信イベントは `{ "type": "...", "data": ... }` の形式で、`session`、`progress`、`trace`、`error`、`ended` を返す。ブラウザーは指数バックオフで再接続する。切断したメッセージの自動再送やAI処理の自動再実行は行わない。CI待機中は接続後にキーなしの `resume` を自動送信し、CI確認だけを続ける。

コマンドと状態の検証は [websocket.go](../backend/internal/httpapi/websocket.go)、フロントエンドの送信処理は [workspace.ts](../frontend/web/src/stores/workspace.ts) を参照する。

## 動作確認

固定デモでは通常セッションで「PRを取得して」「このPRをレビューして」と送る。レビュー計画、base/head、処理位置、未確認範囲を確認できる。デモは実コードを判定せず、指摘は0件とする。

実装はチャットまたは「実装を依頼」から開始し、計画承認、差分確認、Draft PR作成、CI確認を順に確認する。

自動チェックはCIと同じコマンドを使う。以下の各コードブロックはリポジトリルートから実行する。

```sh
cd backend
go test -race ./...
go vet ./...
```

```sh
cd frontend/web
npm ci
npm test
npm run build:local
```

```sh
docker compose up --build -d --wait firebase-emulator
cd backend
RUN_EMULATOR_TESTS=1 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9197 FIRESTORE_EMULATOR_HOST=127.0.0.1:8186 go test -race ./internal/httpapi -run TestEmulator -v
```

単体テストは外部接続をスタブに置き換える。Emulator統合テストは実WebSocketとFirestoreを使い、ユーザー分離、停止・再開、レビュー・実装の保存と復元を確認する。累計予算、ドラフト保持・延長、編集の予算配分は単体テストとEmulator統合テストで確認する。実GitHub・実AIとの互換性や生成品質は、資格情報のある環境で別途確認する。

## ログ

`docker compose logs api` でJSONログを確認できる。画面とWebSocketの `trace` に実行ごとの追跡IDを返し、`trace_id` で実行、`session_id` で会話を関連付ける。追跡IDはブラウザーの再読み込みでは復元しない。

実行・モデル呼び出し・GitHub取得の開始と完了、所要時間、利用トークン、残り予算、実装の段階や再試行回数を記録する。通常チャットの `chat_final_answer_required` は回答への切替、`github_read_rejected` は取得上限による拒否、`chat_answer_fallback` は保存済みメモでの終了を示す。通常ログには本文、キー、コード、プロバイダーの任意のエラー本文を残さない。

### ローカル限定の詳細debugログ

`AGENT_DEBUG_LOG=1` でAPIを起動すると、依頼文、取得・生成コード、モデルとプロバイダーの入出力を含む詳細ログを出力する。本番設定とCloud Runでは有効化を拒否する。既知の認証情報はマスクするが、共有する前に本文を確認する。

```sh
AGENT_DEBUG_LOG=1 docker compose up --build -d --wait --no-deps api
docker compose logs --no-color --since 10m api | python3 scripts/read-agent-debug.py > /tmp/ai-factory-debug.jsonl
```

`ai_call_id` でモデル呼び出しを追い、分割したpayloadは `payload_id` と `chunk_index/chunk_count` で復元する。欠けたレコードがあると復元スクリプトは失敗するため、ログの取得期間を広げる。編集拒否の調査では期待操作・提出操作、原文ハッシュ、行範囲の期待・実際の境界と周辺コードを確認できる。
