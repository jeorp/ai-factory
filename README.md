# AI Factory

GitHubの情報を読み取り専用で参照し、PRの差分を分割レビューするチャットアプリです。[AIファクトリー PRレビュー MVP 仕様書](https://chatgpt.com/space/page_fac25bd05bb48191a477ebdc827f840f)の第一段階（GitHubチャット）と第二段階のPR取得・レビュー計画・分割確認・全体照合・結果表示を実装しています。

`../system-design-skill-developer` に合わせた Vue 3 / TypeScript / Vue Router / Pinia、Go、Firebase Auth / Firestore、Docker Composeを使用します。AIはOpenAIまたはGemini Developer APIを選択できます。WebSocketと認証Cookieを同じOriginで扱うため、公開時はVueのビルド成果物もGo APIと同じCloud Runサービスから配信します。

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

通常のリポジトリの会話でPRを取得したあと、そのまま「このPRをレビューして」と送ると分割レビューを開始します。「PR #123 をレビューして」と番号を指定することもできます。取得したPR番号はセッションに保存され、既存の会話でも参照元から対象を引き継ぎます。対象が分からない場合は同じチャットでPR番号を確認します。ベース・ヘッドのSHA、最大10ファイルのグループ計画、差分の処理位置、指摘、未確認事項を保存します。指摘には重要度・ファイル／行・問題・影響・根拠・修正案を表示します。PRが更新されたら中断し、チャットで再レビューを依頼すると最新の対象を取得します。

未確認の範囲・制限は、各項目を表示する前にAIが優先度を高・中・低・未評価で確認し、理由とともに優先度順に表示します。評価できない項目も残します。既存レビューは未評価として表示します。各項目の追加調査結果は会話とレビューに保存します。優先度の確認にもAI呼び出しを使用します。

PRレビューは関連する差分を最大10ファイル、約2万トークンずつまとめて確認します。入力全体は約3万トークンを目安に調整します（o200k_baseによる共通推定で、モデル固有の実トークン数とは異なります）。20回ごとの手動再開はなくし、最大15分・AI呼び出し100回・入力50万／出力6.4万トークンまで自動で続けます。上限・停止・切断後は「保存済みの状態から再開」で続けます。通常チャットの1回の実行は最大3分です。先頭200ファイルまでを計画し、取得不能・バイナリ・入力上限などの未確認範囲を明示します。PRメタデータ・ページ単位の差分にはGitHub REST API、関連コード・コメントには読み取り専用MCPを利用します。

指摘選択・編集・GitHubへの投稿、投稿時のヘッド確認・重複投稿防止は未実装です。アプリ側の課金・クレジット管理、Vertex AI、コード実行は含みません。

## 実接続・開発・公開準備

[開発・実接続ガイド](docs/development.md)、[設計と制限](docs/architecture.md)、[Cloud Run配置](docs/deployment.md)を参照してください。実接続には利用者が所有するGitHub Appと、選択したAIのAPIキーが必要です。認証情報の登録やクラウドへのデプロイは行っていません。

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

### Gemini Developer APIを試す

`.env` に `AI_PROVIDER=gemini` と `GEMINI_MODELS=gemini-3.5-flash-lite` を設定し、`docker compose up --build -d` で再起動します。画面でGoogle AI StudioのGemini APIキーを入力してください。キーは環境変数に保存せず、セッションごとに一時的に利用します。GitHubも固定デモにするには `PROVIDER=demo` を指定します。

無料枠の利用可否や上限はGoogleのプロジェクトとモデルに依存します。課金済みプロジェクトでは料金が発生する場合があります。無料枠では送信した情報がGoogleのサービス改善に使用される場合があるため、非公開コードを送る前に利用条件を確認してください。Vertex AIとは別のAPIです。
