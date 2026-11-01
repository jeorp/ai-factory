# Cloud Run配置

公開・クラウドリソース作成は未実施です。以下は利用するGCPプロジェクトと認証情報を決めた後の設定手順です。公開済みのToCサービスとして運用開始した状態ではありません。

## 構成

Vueの成果物とGo APIを一つのCloud Runサービスで配信し、OAuth callback・Cookie・WebSocketを同一Originにそろえます。ローカルではViteが `/api`（WebSocket含む）と `/auth` をGoへproxyします。

Firebase HostingをWebSocket経路の前提にしない構成です。隣のプロジェクトからVue・Go・Firebase Auth・Firestoreを合わせつつ、接続型MVPの配信方式を変更しています。Firebase HostingへのWeb配信設定は含めません。

## 設定

1. Firebaseプロジェクト・Firestore Nativeを作成し、Firebase Authenticationを有効にする。GitHub providerは使わず、サーバーからのカスタムトークン認証を使う。
2. 専用サービスアカウントにFirestore読み書きと、Firebaseカスタムトークン署名に必要な自身への `iam.serviceAccounts.signBlob` 権限を与える。一般的には `roles/datastore.user` と、対象サービスアカウント自身への `roles/iam.serviceAccountTokenCreator` を使用する。秘密鍵JSONを配布しない。
3. GitHub Appの権限とcallbackを設定し、選択したリポジトリにインストールする。
   - callbackは `https://APP_DOMAIN/auth/callback`。
   - 実装・PR作成を使う場合はContentsとPull requestsをRead and writeにする（Workflowsは付けない）。
   - PRのCI確認および失敗ログ参照のためにChecks (Read) と Actions (Read) を追加する（書き込み権限は不要）。
   - 権限を追加した場合、既存のインストールでは利用者による承認が必要。
4. GitHub client secretと `openssl rand -base64 32` で生成したCookie鍵をSecret Managerに保存し、実行サービスアカウントだけに読み取りを許可する。
5. Dockerビルド時に `VITE_FIREBASE_PROJECT_ID`、`VITE_FIREBASE_API_KEY`、`VITE_FIREBASE_AUTH_DOMAIN` をbuild argsで渡す。これらはブラウザー公開設定であり、OpenAIキーやGitHub secretを渡さない。既定の `WEB_BUILD_MODE=production` のままビルドする。
6. `infra/cloud-run/service.yaml` の `REPLACE_*` を置換し、WEB_ORIGINを実際のHTTPS Originへ設定する。timeoutは3600秒。Goは55分でWebSocketを更新する。コンテナは非rootで起動する。
7. Firestoreルール・TTL設定を反映する。CLI例: `firebase deploy --only firestore --project PROJECT_ID`。sessions と githubReadCache の ExpiresAt のTTLが有効か管理画面でも確認する。

## 配置後の確認

次の項目を実接続で検証してください。

- 同一OriginのWeb表示
- GitHubログイン
- Appの許可リポジトリだけの参照
- OpenAI回答
- 停止・切断・再接続
- キー期限
- 別ユーザーからのアクセス拒否

認証Cookieは最大8時間で再ログインし、GitHub refresh tokenは保存しません。

## 公開前に残る作業

- 実GitHub App + GitHub remote MCP + OpenAIでの権限・応答互換性の確認。
- 大きなリポジトリ・長い会話でのピークメモリ、入力トークン、料金の測定。
- 利用規約、プライバシー説明、OpenAIへのデータ提供条件、削除対応の整備。
- 必要な監視・リクエスト数制限・不正利用対策・バックアップ／TTL運用の確認。
- レビュー指摘のGitHub投稿（指摘選択・編集、ヘッド再確認、冪等投稿）。
- 実GitHub Appでの実装・PR作成の確認（書き込み権限の承認フロー、ブランチ保護、大きなファイル・多数ファイルの変更、AIの検索・置換の適用率）。
