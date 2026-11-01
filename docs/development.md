# 開発・実接続ガイド

ローカルで AI Factory を動かし、実際の GitHub と AI プロバイダーへ接続するための手順をまとめる。固定デモでの起動はルートの [README](../README.md#run-locally) を参照する。設計と上限値は[設計と制限](architecture.md)にある。

| やりたいこと | 参照する節 |
| --- | --- |
| 自分のリポジトリを読ませる | [GitHubの実データに接続する](#githubの実データに接続する) |
| OpenAI以外のAIを使う | [AIプロバイダーの設定](#aiプロバイダーの設定) |
| Go APIをDockerなしで動かす | [Go APIを直接起動する](#go-apiを直接起動する) |
| APIやWebSocketの仕様を知る | [API / WebSocket](#api--websocket) |
| 実行ログを追う | [会話処理の追跡](#会話処理の追跡) |
| PRレビューや実装を試す | [動作確認](#動作確認) |

## GitHubの実データに接続する

1. GitHub Appを作成し、Repository permissionsを次のように設定する。User access tokenの有効期限を有効にする。
   - **Metadata / Issues**: Read-only
   - **Contents / Pull requests**: Read and write。書き込み権限は実装・PR作成（利用者が確認して実行した場合のみ）に使う。読み取りだけで使う場合はRead-onlyでもよく、その場合PR作成は `repository_write_forbidden` で拒否される。
   - **Checks / Actions**: Read-only。既存PRへの修正依頼でCIの失敗ログを読むために使う。
   - Workflowsの権限は付けない。
2. Callback URLを `http://127.0.0.1:5175/auth/callback` にする。OAuth AppではなくGitHub AppのClient ID / Client secretを使う。サーバーはGitHub Appの `ghu_` user access tokenだけを受け付ける。
3. 利用するリポジトリにGitHub Appをインストールする。Appと本人の両方がアクセスできる範囲だけを参照できる。
4. ルートの `.env.example` を `.env` にコピーし、`PROVIDER=live`、`GITHUB_CLIENT_ID`、`GITHUB_CLIENT_SECRET`、`GITHUB_APP_SLUG` を設定する。実キーをソース管理しない。
5. `docker compose up --build -d --wait` で起動する。Web側は `npm run dev`。
6. GitHubでログインし、セッションを作成する。送信先と費用の説明を確認し、自分のOpenAI APIキーを画面で入力する。APIキーを `.env` や会話には書かない。

## AIプロバイダーの設定

どのプロバイダーも `.env` を変更したらComposeを再ビルドして起動する。キーやトークンは環境変数ではなく画面から入力する。

| プロバイダー | `AI_PROVIDER` | モデルの許可リスト | 画面で入力するもの |
| --- | --- | --- | --- |
| OpenAI | `openai`（`PROVIDER=demo` 以外で未設定なら既定） | `OPENAI_MODELS`（既定 `gpt-5-mini`） | OpenAI APIキー |
| Gemini Developer API | `gemini` | `GEMINI_MODELS` | Google AI StudioのAPIキー |
| Vertex AI経由のClaude | `vertex` | `CLAUDE_MODELS` | `gcloud auth print-access-token` のアクセストークン |
| Anthropic API | `anthropic` | `ANTHROPIC_MODELS` | Anthropic ConsoleのAPIキー（`sk-ant-…`） |

### OpenAI

モデルはサーバーの `OPENAI_MODELS` の許可リストで選択肢を設定する。既定は `gpt-5-mini`。Responses APIの構造化出力と `reasoning.effort=minimal` に対応するモデルを指定する。モデル変更時はAPI互換性を確認する。モデルの利用資格や利用枠は各OpenAIアカウントに依存する。

### Gemini Developer API（無料枠）

`.env` に `AI_PROVIDER=gemini`、`GEMINI_MODELS=gemini-3.5-flash-lite` を設定し、Composeを再ビルドして起動する。Google AI Studioで取得したキーを画面から入力する。`PROVIDER=demo` と組み合わせるとGitHubはサンプル、AIは実際のGeminiとなる。

無料枠の制限はGoogleのプロジェクトとモデルに依存し、課金済みプロジェクトでは料金が発生し得る。無料枠で送信したデータはGoogleのサービス改善に使われる場合がある。

画面の「AIモデル」には `GEMINI_MODELS`（カンマ区切り、先頭が既定）の許可リストが表示される。未設定時は `gemini-3.5-flash-lite`、`gemini-3.8-flash`、`gemini-3.1-pro-preview` の3つ。`gemini-4-argon` や `gemini-2.5-pro` なども追加できる。

- Proモデル（`gemini-3.1-pro-preview`）とGemini 4 Argonには無料枠がないため、課金を有効にしたプロジェクトのキーが必要。無料枠のキーでは利用枠エラーになる。
- Gemini 3以降のモデルには `thinkingLevel=low` を指定し、思考トークンを抑える。`minimal` はProで使えないため全モデル共通で `low` を使う。Gemini 2.xは既定の思考設定のまま送る。
- Geminiは思考トークンを `maxOutputTokens` に含めるため、回答枠に8192トークンの余裕を加えている。思考トークンも出力として課金される。
- 料金表示（`frontend/web/src/lib/pricing.ts`）は2026-10-31時点の200kトークン以下の単価。未登録のモデルは料金を推定しない。

### Vertex AI経由のClaude

`.env` に `AI_PROVIDER=vertex`（Geminiと併用する場合は `AI_PROVIDER=gemini,vertex`）、`VERTEX_PROJECT_ID=<GCPプロジェクトID>` を設定し、Composeを再ビルドして起動する。`VERTEX_REGION` の既定は `global`、`CLAUDE_MODELS`（カンマ区切り、先頭が既定）の既定は `claude-haiku-5-5,claude-sonnet-5-5`。

1. GCPプロジェクトでVertex AI APIを有効にし、Model Gardenで使うClaudeモデル（例: Claude Haiku 5.5）を開いてEnableする（利用規約への同意が必要）。
2. 利用するGoogleアカウントに `roles/aiplatform.user` を付与する。
3. ターミナルで `gcloud auth print-access-token` を実行し、表示されたアクセストークンを画面に入力する。サーバーはGoogleの認証情報を保持せず、トークンは約1時間で失効する。

- Claudeには `output_config.effort=low` と構造化出力（JSON Schema）を指定し、思考トークンを含めるため `max_tokens` に8192トークンの余裕を加えている。
- 401はトークン失効（`claude_token_invalid`）、404はモデル未Enableまたはリージョン・モデル名の誤り（`claude_model_unavailable`）として表示する。
- Vertex AIのClaude料金はGoogleの料金表に従う。料金表示には未登録のため、費用は推定しない。

### Anthropic APIで直接Claude

`.env` に `AI_PROVIDER=anthropic`（併用する場合は `AI_PROVIDER=gemini,vertex,anthropic`）を設定し、Composeを再ビルドして起動する。Anthropic Consoleで発行したAPIキー（`sk-ant-…`）を画面から入力する。モデルは `ANTHROPIC_MODELS`（既定 `claude-haiku-5-5,claude-sonnet-5-5`）。料金表示はClaude APIの単価（100kトークン以下）で推定する。

### 複数のプロバイダーを併用する

- `AI_PROVIDER` にカンマ区切りで複数のプロバイダーを並べると、画面の「AIモデル」に全モデルが並び、選んだモデルのプロバイダーで実行する。キー入力欄もモデルに合わせてGeminiのAPIキーとVertex AIのトークンを切り替える。
- Vertex AIとAnthropic APIを併用すると同じClaudeモデルが両方に並ぶため、Vertex AIのモデルは `vertex/` を付けたID（例 `vertex/claude-haiku-5-5`）で区別する。画面では「（Vertex AI）」「（Anthropic API）」と表示する。

## Go APIを直接起動する

```sh
docker compose up -d --wait firebase-emulator
cd backend
APP_ENV=local PROVIDER=demo GCP_PROJECT_ID=demo-ai-factory WEB_ORIGIN=http://127.0.0.1:5175 PORT=8181 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9197 FIRESTORE_EMULATOR_HOST=127.0.0.1:8186 go run ./cmd/api
```

Docker版APIと同時に起動しない。ローカル設定は `demo-` プロジェクト・両Emulator・loopback Originを必須にする。本番は逆にEmulatorと固定デモを拒否する。

## API / WebSocket

### HTTP API

HTTPは `{ "error": "code" }` でエラーを返す。個人APIはFirebase ID tokenのBearerとGitHub用HttpOnly CookieのUID一致を必須とし、書き込みはOriginを検証する。

| Method | Path | 内容 |
| --- | --- | --- |
| GET | `/api/config` | デモ状態、モデル、App設定URL |
| GET | `/auth/github` | state / PKCE付きOAuth開始 |
| GET | `/auth/callback` | コード交換、暗号化Cookie、秘密を含まないリダイレクト |
| POST | `/auth/complete` | 同一Origin限定でFirebaseカスタムトークン取得 |
| POST | `/auth/logout` | GitHub Cookie破棄 |
| GET | `/api/v1/me` | ログイン名 |
| GET | `/api/v1/repositories` | Appと本人がアクセスできるリポジトリ |
| GET / POST | `/api/v1/sessions` | 直近40セッションの一覧／作成 |
| GET / DELETE | `/api/v1/sessions/{id}` | 本人のセッション取得／削除 |
| GET | `/api/v1/sessions/{id}/ws` | WebSocketアップグレード |

変更後のファイル本文は `GET /api/v1/sessions/{id}/change?path=...` で元ファイルと一緒に返す。

### WebSocketの接続

WebSocket最初のフレームは `{type:"auth",token:"Firebase ID token"}`。認証情報をURLに入れない。サーバーが認証・lease取得後、`session` を返す。

### ブラウザーから送るメッセージ

- `message`: `{type,id,text,apiKey,keyExpiresAt}`。IDはUUID等16〜80文字。保存済みIDの再送は再実行しない。キーは保存対象とは別の変数で扱う。
- `instruction`: `{type,id,text}`。受け付けた指示を保存し、AI呼び出し間で反映する。受付／反映済みを区別する。
- `stop`: キャンセル要求。`stopping` → `stopped`。停止後の遅い応答は保存しない。
- `resume`: キーを伴う明示操作で保存済みの要点・履歴から続行する。外部読み取りは再取得される場合がある。実装では保存した区切りから計画・変更作成・PR作成を再開する。
- `end`: 接続終了。実行を止め、ブラウザーのキーを破棄する。

実装には次のコマンドを使う。

- `implement`: 依頼文。AIキー必須。
- `implement_approve`: 計画の承認。AIキー必須。
- `implement_publish`: `branch` / `title` / `body`。AIキー不要。

### サーバーから届くイベント

受信イベントは `session`（保存済み状態）、`progress`（処理説明）、`error`、`ended`。ブラウザーは指数バックオフで再接続する。切断したメッセージの自動再送・自動AI再実行は行わない。

## 会話処理の追跡

各質問・再開に追跡 ID を発行し、画面と WebSocket の `trace` イベントに表示する。ID は実行中のブラウザーに表示され、再読み込み後には復元されない。

API の標準出力には JSON ログが出力され、`trace_id` で一回の実行、`session_id` で同じ会話の複数の実行を関連付けられる。

| イベント | 記録する内容 |
| --- | --- |
| `run_started` / `run_finished` | 実行の開始と終了。終了イベントには所要時間と結果を含む |
| `model_call_started` / `model_call_finished` | モデル呼び出し。トークン数を含む |
| `github_read_started` / `github_read_finished` | GitHubからの取得 |
| キャッシュ利用 | 取得結果のキャッシュ利用 |

通常チャットの取得・キャッシュ利用・拒否ログには `kind`、`path`、`number`（PR・Issue番号）、`detail` と、その判断時点の `remaining_reads`・`remaining_files`・`remaining_model_calls` を含める。質問や回答、取得内容、キー、外部サービスのエラー本文は記録しない。

通常チャットでは次のイベントも記録する。

- `chat_final_answer_required`: 最終判断に切り替えたとき。
- `chat_answer_completed`: 回答完了時。
- `github_read_rejected`: AIが枠を超えた取得を要求した場合。対象と `reason`（`read_limit`・`file_limit`・`model_call_limit`）を記録し、取得を実行しない。
- `chat_answer_fallback`: 最終判断でも取得を要求した場合。保存済みの調査メモと未確認の案内で終了する。

ファイル上限だけに達した場合は、残りの判断枠で回答を続ける。

ローカルの Compose では `docker compose logs api` で確認する。本番では API の標準出力を収集するログ基盤で `trace_id` または `session_id` を検索する。これは構造化ログによる相関であり、外部サービス内部までの分散トレースではない。

## 動作確認

### PRレビュー

固定デモでは `demo/ai-factory` の通常セッションで「PRを取得して」と送ったあと、「このPRをレビューして」と送る。同じ会話で2ディレクトリの計画、ベース／ヘッド、処理状況、未確認範囲を確認できる。デモは実コードを判定せず、指摘は0件とする。

実接続ではGitHub Appに対象リポジトリのContents・Pull requestsの読み取り権限を付与する。PRメタデータとページごとの差分はREST、周辺コードとコメントはMCPで読む。利用するAIはチャットと同じOpenAIまたはGemini。

- 停止・切断・時間上限後は再接続して「保存済みの状態から再開」を押す。20回のモデル呼び出しに達した場合も同じ操作で続行する。
- PR更新時にはチャットで再レビューを依頼して計画を作り直す。

テストの範囲は次のとおり。

- 単体テスト: 大きな差分の分割・元行番号・カーソル再開・保存失敗・PR更新・根拠不足の指摘・重複・取得上限・認証情報のエコー拒否。
- Emulator統合テスト: 実WebSocket経由のレビュー停止／再開とFirestore復元。
- 実GitHubと実AIを組み合わせたレビュー品質・大規模PRのレート制限／負荷は別途検証する。

レビューの仕組みは[設計と制限のPRレビュー](architecture.md#prレビュー)にある。

### 実装・PR作成

1. チャットで「〜を実装してPRを作って」と依頼するか、メッセージの種類で「実装を依頼」を選ぶ。
2. AIは基準コミットのコードを読んで実装計画を返す。利用者が「計画を承認して変更を作成」を押すまで変更を作らない。
3. 変更は最大5ファイルずつ検索・置換ブロックで作成し、サーバーが元ファイルへの一意な一致を確認して適用する。
4. 差分を確認し、ブランチ名・PRタイトル・本文を確認して「Draft PRを作成」を押すとGitHubへ書き込む。PR作成はAIキーを使わない。

計画・変更・整合性確認・CI確認の詳細は[設計と制限の実装・PR作成](architecture.md#実装pr作成)にある。

Emulator統合テスト `TestEmulatorImplementationLifecycle` は固定デモのAIとGitHubで、計画・承認・差分・不正なブランチ名や秘密情報の拒否・PR作成・Firestoreへの保存を実WebSocket経由で確認する。Emulatorはfirebase-toolsで起動できる。

```sh
npx -y firebase-tools@13.29.1 emulators:start --only auth,firestore --project demo-ai-factory
cd backend && RUN_EMULATOR_TESTS=1 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099 FIRESTORE_EMULATOR_HOST=127.0.0.1:8085 go test ./internal/httpapi -run Emulator
```

## 参照した公式資料

- [OpenAI Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs)
- [GPT-5 mini](https://developers.openai.com/api/docs/models/gpt-5-mini)
- [GitHub App user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
- [GitHub MCP configuration](https://github.com/github/github-mcp-server/blob/main/docs/server-configuration.md)
