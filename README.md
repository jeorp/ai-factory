# AI Factory

GitHubの情報を読み取り専用で参照し、根拠と未確認範囲を示すチャットアプリです。[AIファクトリー PRレビュー MVP 仕様書](https://chatgpt.com/space/page_fac25bd05bb48191a477ebdc827f840f)の**第一段階（MVP）**を実装しています。

`../system-design-skill-developer` に合わせた Vue 3 / TypeScript / Vue Router / Pinia、Go、Firebase Auth / Firestore、Docker Composeを使用します。AIは仕様どおりOpenAI APIです。WebSocketと認証Cookieを同じOriginで扱うため、公開時はVueのビルド成果物もGo APIと同じCloud Runサービスから配信します。

## ローカル起動

必要: Docker、Go 1.27.2以降、Node.js 22.12以上の対応LTS（22または24）、npm。

```sh
docker compose up --build -d --wait
cd frontend/web
npm ci
npm run dev
```

[アプリ](http://127.0.0.1:5175/)を開き、「開発用ログイン」→「セッションを作成」→質問を送信してください。**既定は固定デモ**です。実際のGitHubやOpenAIへ接続せず、APIキーも不要です。Firebase Emulatorには実際に認証・保存します。デモの参照先URLもサンプルであり、実在するコードの根拠ではありません。

停止: WebのターミナルでCtrl+C、ルートで `docker compose down`。Emulatorのデータは専用volumeに保存します。他プロジェクトとポート・データを分離しています。

| サービス | URL |
| --- | --- |
| Web | http://127.0.0.1:5175 |
| API | http://127.0.0.1:8181 |
| Emulator UI | http://127.0.0.1:4101 |
| Firebase Auth / Firestore | 127.0.0.1:9197 / 127.0.0.1:8186 |

## 実装範囲

- GitHub App OAuth（state・PKCE）、Firebaseカスタムトークンによる本人認証。
- GitHub Appに許可したリポジトリの一覧、リポジトリ・PR・Issue URLからセッション作成。
- 読み取り専用GitHub MCP接続。サーバーが対象リポジトリ・コミット・操作を固定。
- OpenAI Responses APIの構造化出力を使った段階的な情報取得と回答。
- WebSocketでの質問・追加指示・停止、進捗表示、切断時の中断、再接続後の会話復元と手動再開。
- 取得結果と保存履歴の分離、直近の会話＋要点へのコンテキスト制限、同一実行内の重複取得抑制。
- OpenAIキーはブラウザーと実行中のサーバーのメモリのみ。最大1時間、終了・ログアウト・ページ終了時に破棄。
- Firestoreのユーザー分離、接続leaseによる二重実行防止、30日の保存期限、本人による削除。
- 取得回数・バイト数・入力／出力トークン・処理時間・プロセスヒープ観測値の表示。

**第二段階**の自動PRレビュー、レビュー計画・全体照合、指摘選択・編集・投稿、最新ヘッド確認、重複投稿防止は未実装です。課金・クレジット・Vertex AI・コード実行も含みません。

## 実接続・開発・公開準備

[開発・実接続ガイド](docs/development.md)、[設計と制限](docs/architecture.md)、[Cloud Run配置](docs/deployment.md)を参照してください。実接続には利用者が所有するGitHub AppとOpenAI APIキーが必要です。認証情報の登録やクラウドへのデプロイは行っていません。

## 検証

```sh
cd backend
go test -race ./...
go vet ./...
# 先にルートで docker compose up -d --wait firebase-emulator
RUN_EMULATOR_TESTS=1 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9197 FIRESTORE_EMULATOR_HOST=127.0.0.1:8186 go test -race ./internal/httpapi -run TestEmulator -v
cd ../frontend/web
npm test
npm run build:local
```

実際のGitHub App認証・GitHub MCP・OpenAIを組み合わせたE2Eは、認証情報を設定した環境で別途実施してください。通常テストでは外部通信をスタブに置き換え、統合テストではFirebase Auth / Firestore Emulatorと実WebSocket接続を使います。
