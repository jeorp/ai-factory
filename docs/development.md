## Gemini Developer API（無料枠）

`.env` に `AI_PROVIDER=gemini`、`GEMINI_MODELS=gemini-3.5-flash-lite` を設定し、Composeを再ビルドして起動する。Google AI Studioで取得したキーを画面から入力する。`PROVIDER=demo` と組み合わせるとGitHubはサンプル、AIは実際のGeminiとなる。無料枠の制限はGoogleのプロジェクトとモデルに依存し、課金済みプロジェクトでは料金が発生し得る。無料枠で送信したデータはGoogleのサービス改善に使われる場合がある。

### Geminiモデルの選択

画面の「AIモデル」には `GEMINI_MODELS`（カンマ区切り、先頭が既定）の許可リストが表示される。未設定時は `gemini-3.5-flash-lite`、`gemini-3.8-flash`、`gemini-3.1-pro-preview` の3つ。`gemini-4-argon` や `gemini-2.5-pro` なども追加できる。

- Proモデル（`gemini-3.1-pro-preview`）とGemini 4 Argonには無料枠がないため、課金を有効にしたプロジェクトのキーが必要。無料枠のキーでは利用枠エラーになる。
- Gemini 3以降のモデルには `thinkingLevel=low` を指定し、思考トークンを抑える。`minimal` はProで使えないため全モデル共通で `low` を使う。Gemini 2.xは既定の思考設定のまま送る。
- Geminiは思考トークンを `maxOutputTokens` に含めるため、回答枠に8192トークンの余裕を加えている。思考トークンも出力として課金される。
- 料金表示（`frontend/web/src/lib/pricing.ts`）は2026-10-31時点の200kトークン以下の単価。未登録のモデルは料金を推定しない。

## Vertex AI経由のClaude

`.env` に `AI_PROVIDER=vertex`、`VERTEX_PROJECT_ID=<GCPプロジェクトID>` を設定し、Composeを再ビルドして起動する。`VERTEX_REGION` の既定は `global`、`CLAUDE_MODELS`（カンマ区切り、先頭が既定）の既定は `claude-haiku-5-5,claude-sonnet-5-5`。

1. GCPプロジェクトでVertex AI APIを有効にし、Model Gardenで使うClaudeモデル（例: Claude Haiku 5.5）を開いてEnableする（利用規約への同意が必要）。
2. 利用するGoogleアカウントに `roles/aiplatform.user` を付与する。
3. ターミナルで `gcloud auth print-access-token` を実行し、表示されたアクセストークンを画面に入力する。サーバーはGoogleの認証情報を保持せず、トークンは約1時間で失効する。

- Claudeには `output_config.effort=low` と構造化出力（JSON Schema）を指定し、思考トークンを含めるため `max_tokens` に8192トークンの余裕を加えている。
- 401はトークン失効（`claude_token_invalid`）、404はモデル未Enableまたはリージョン・モデル名の誤り（`claude_model_unavailable`）として表示する。
- Vertex AIのClaude料金はGoogleの料金表に従う。料金表示には未登録のため、費用は推定しない。

# 開発・実接続

## 実データを読む

1. GitHub Appを作成し、Repository permissionsを **Metadata / Issues: Read-only**、**Contents / Pull requests: Read and write**、**Checks / Actions: Read-only** に設定する。ChecksとActionsは既存PRへの修正依頼でCIの失敗ログを読むために使う。書き込み権限は実装・PR作成（利用者が確認して実行した場合のみ）に使う。読み取りだけで使う場合はRead-onlyでもよく、その場合PR作成は `repository_write_forbidden` で拒否される。Workflowsの権限は付けない。User access tokenの有効期限を有効にする。
2. Callback URLを `http://127.0.0.1:5175/auth/callback` にする。OAuth AppではなくGitHub AppのClient ID / Client secretを使う。サーバーはGitHub Appの `ghu_` user access tokenだけを受け付ける。
3. 利用するリポジトリにGitHub Appをインストールする。Appと本人の両方がアクセスできる範囲だけを参照できる。
4. ルートの `.env.example` を `.env` にコピーし、`PROVIDER=live`、`GITHUB_CLIENT_ID`、`GITHUB_CLIENT_SECRET`、`GITHUB_APP_SLUG` を設定する。実キーをソース管理しない。
5. `docker compose up --build -d --wait` で起動する。Web側は `npm run dev`。
6. GitHubでログインし、セッションを作成する。送信先と費用の説明を確認し、自分のOpenAI APIキーを画面で入力する。APIキーを `.env` や会話には書かない。

モデルはサーバーの `OPENAI_MODELS` の許可リストで選択肢を設定する。既定は `gpt-5-mini`。Responses APIの構造化出力と `reasoning.effort=minimal` に対応するモデルを指定する。モデル変更時はAPI互換性を確認する。モデルの利用資格や利用枠は各OpenAIアカウントに依存する。

## Go APIを直接起動

```sh
docker compose up -d --wait firebase-emulator
cd backend
APP_ENV=local PROVIDER=demo GCP_PROJECT_ID=demo-ai-factory WEB_ORIGIN=http://127.0.0.1:5175 PORT=8181 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9197 FIRESTORE_EMULATOR_HOST=127.0.0.1:8186 go run ./cmd/api
```

Docker版APIと同時に起動しない。ローカル設定は `demo-` プロジェクト・両Emulator・loopback Originを必須にする。本番は逆にEmulatorと固定デモを拒否する。

## API / WebSocket

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

WebSocket最初のフレームは `{type:"auth",token:"Firebase ID token"}`。認証情報をURLに入れない。サーバーが認証・lease取得後、`session` を返す。

- `message`: `{type,id,text,apiKey,keyExpiresAt}`。IDはUUID等16〜80文字。保存済みIDの再送は再実行しない。キーは保存対象とは別の変数で扱う。
- `instruction`: `{type,id,text}`。受け付けた指示を保存し、AI呼び出し間で反映する。受付／反映済みを区別する。
- `stop`: キャンセル要求。`stopping` → `stopped`。停止後の遅い応答は保存しない。
- `resume`: キーを伴う明示操作で保存済みの要点・履歴から続行する。外部読み取りは再取得される場合がある。
- `end`: 接続終了。実行を止め、ブラウザーのキーを破棄する。

受信イベント: `session`（保存済み状態）、`progress`（処理説明）、`error`、`ended`。ブラウザーは指数バックオフで再接続する。切断したメッセージの自動再送・自動AI再実行は行わない。

## 会話処理の追跡

各質問・再開に追跡 ID を発行し、画面と WebSocket の `trace` イベントに表示する。API の標準出力には JSON ログが出力され、`trace_id` で一回の実行、`session_id` で同じ会話の複数の実行を関連付けられる。`run_started` / `run_finished`、`model_call_started` / `model_call_finished`、`github_read_started` / `github_read_finished`、キャッシュ利用を記録する。終了イベントには所要時間と結果、モデル呼び出しにはトークン数を含む。通常チャットの取得・キャッシュ利用・拒否ログには `kind`、`path`、`number`（PR・Issue番号）、`detail` と、その判断時点の `remaining_reads`・`remaining_files`・`remaining_model_calls` を含める。質問や回答、取得内容、キー、外部サービスのエラー本文は記録しない。ID は実行中のブラウザーに表示され、再読み込み後には復元されない。

通常チャットでは、最終判断に切り替えたときに `chat_final_answer_required`、回答完了時に `chat_answer_completed` を記録する。AIが枠を超えた取得を要求した場合は `github_read_rejected` に対象と `reason`（`read_limit`・`file_limit`・`model_call_limit`）を記録し、取得を実行しない。最終判断でも取得を要求した場合は、保存済みの調査メモと未確認の案内で終了し、`chat_answer_fallback` を記録する。ファイル上限だけに達した場合は、残りの判断枠で回答を続ける。

ローカルの Compose では `docker compose logs api` で確認する。本番では API の標準出力を収集するログ基盤で `trace_id` または `session_id` を検索する。これは構造化ログによる相関であり、外部サービス内部までの分散トレースではない。

## 参照した公式資料

- [OpenAI Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs)
- [GPT-5 mini](https://developers.openai.com/api/docs/models/gpt-5-mini)
- [GitHub App user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
- [GitHub MCP configuration](https://github.com/github/github-mcp-server/blob/main/docs/server-configuration.md)

## PRレビューの確認

固定デモでは `demo/ai-factory` の通常セッションで「PRを取得して」と送ったあと、「このPRをレビューして」と送る。同じ会話で2ディレクトリの計画、ベース／ヘッド、処理状況、未確認範囲を確認できる。デモは実コードを判定せず、指摘は0件とする。

実接続ではGitHub Appに対象リポジトリのContents・Pull requestsの読み取り権限を付与する。PRメタデータとページごとの差分はREST、周辺コードとコメントはMCPで読む。利用するAIはチャットと同じOpenAIまたはGemini。停止・切断・時間上限後は再接続して「保存済みの状態から再開」を押す。20回のモデル呼び出しに達した場合も同じ操作で続行する。PR更新時にはチャットで再レビューを依頼して計画を作り直す。

単体テストは大きな差分の分割・元行番号・カーソル再開・保存失敗・PR更新・根拠不足の指摘・重複・取得上限・認証情報のエコー拒否を確認する。Emulator統合テストは実WebSocket経由のレビュー停止／再開とFirestore復元も確認する。実GitHubと実AIを組み合わせたレビュー品質・大規模PRのレート制限／負荷は別途検証する。

PR取得に成功した番号は `activePullRequest` に保存する。通常メッセージのAI判断が `startReview` を返すと、対象・実行モードを先に保存してから分割レビューへ進む。番号0は会話内で最後に取得したPRを参照する。古いセッションは保存済みのPR参照元も使用する。明示的なPR番号の指定も可能で、リポジトリは会話の接続先に固定する。取得失敗時に対象番号は変更しない。専用セッションの作成・レビュー開始ボタンは不要。

## 実装・PR作成

チャットで「〜を実装してPRを作って」と依頼するか、メッセージの種類で「実装を依頼」を選ぶ。AIは基準コミットのコードを読んで実装計画を返し、利用者が「計画を承認して変更を作成」を押すまで変更を作らない。変更は最大5ファイルずつ検索・置換ブロックで作成し、サーバーが元ファイルへの一意な一致を確認して適用する。差分を確認し、ブランチ名・PRタイトル・本文を確認して「Draft PRを作成」を押すとGitHubへ書き込む。PR作成はAIキーを使わない。

公開前の追加依頼では、保存済みの変更を新しいドラフトへ引き継ぎ、固定ベースに対するcreate/update/deleteを維持する。今回編集するファイルには現在の保存内容を渡し、`preserveExisting=true` のファイルは変更不要なら応答から省略できる。同じ内容が返された場合も保持として扱い、保存済みキャッシュを書き直さず編集を進める。未実装ファイルの省略や保存期限切れを完了扱いにはしない。生成後は、保持した内容を含む変更全体を再検証してから公開可能とする。編集応答のpathは現在の工程のファイルだけに制限する。CI修正では、確認済みのPRヘッドにあるファイルも、同時に失敗原因を直す実際の変更がある場合は維持できる。保持だけで失敗したPRを修正済みにしない。

WebSocketコマンド: `implement`（依頼文、AIキー必須）、`implement_approve`（計画の承認、AIキー必須）、`implement_publish`（`branch` / `title` / `body`、AIキー不要）。`resume` は保存した区切りから計画・変更作成・PR作成を再開する。変更後のファイル本文は `GET /api/v1/sessions/{id}/change?path=...` で元ファイルと一緒に返す。

PR作成後は対象コミットのCIを10秒間隔で最大5分確認する。未完了なら `ErrCIWaiting` で実行を区切り、実装の `checking_ci` とセッションの `ci_wait` を保存する。ブラウザーは接続中に10秒後の `resume` を送って確認を続ける。この継続ではAIキーを送らず、モデル呼び出し・再公開・完了メッセージの追加も行わない。切断中はアプリの確認が止まり、再接続後に継続する。`stop` は実行中と区切りの間の待機の両方を停止する。画面は待機状態と固定コミットの `/commit/{sha}/checks` リンクを表示し、ジョブ一覧やログは表示しない。結果取得不能や不完全な結果は未確認として止め、CI成功と区別する。1回の待機（最大5分）を終えてもチェックが1件も登録されない場合は、CIのないリポジトリやブランチでの無限待機を避けるため未確認として止める。`CIWait=0` はデモ・評価用に1回だけスナップショットを取得する。

Emulator統合テスト `TestEmulatorImplementationLifecycle` は固定デモのAIとGitHubで、計画・承認・差分・不正なブランチ名や秘密情報の拒否・PR作成・Firestoreへの保存を実WebSocket経由で確認する。Emulatorはfirebase-toolsで起動できる:

```sh
npx -y firebase-tools@13.29.1 emulators:start --only auth,firestore --project demo-ai-factory
cd backend && RUN_EMULATOR_TESTS=1 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9099 FIRESTORE_EMULATOR_HOST=127.0.0.1:8085 go test ./internal/httpapi -run Emulator
```

