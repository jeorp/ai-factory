# 開発・実接続

## 実データを読む

1. GitHub Appを作成し、Repository permissionsを **Metadata / Contents / Pull requests / Issues: Read-only** に設定する。書き込み権限は付けない。User access tokenの有効期限を有効にする。
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

## 参照した公式資料

- [OpenAI Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs)
- [GPT-5 mini](https://developers.openai.com/api/docs/models/gpt-5-mini)
- [GitHub App user access tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-user-access-token-for-a-github-app)
- [GitHub MCP configuration](https://github.com/github/github-mcp-server/blob/main/docs/server-configuration.md)
