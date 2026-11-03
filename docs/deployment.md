# Cloud Run配置

[Dockerfile](../backend/Dockerfile) でVueの成果物とGo APIを1つのコンテナにまとめ、[Cloud Runのテンプレート](../infra/cloud-run/service.yaml) で配信する。OAuth callback、Cookie、WebSocketは同一Originを使う。

## 認証と保存先

1. Firebaseプロジェクト、Firestore Native、Firebase Authenticationを用意する。認証はサーバーが発行するFirebaseカスタムトークンを使う。
2. 実行用サービスアカウントにFirestoreの読み書きと、カスタムトークン署名に必要な `iam.serviceAccounts.signBlob` を与える。ロールは `roles/datastore.user` と、対象サービスアカウント自身への `roles/iam.serviceAccountTokenCreator` を使う。
3. GitHub Appのcallbackを `https://APP_DOMAIN/auth/callback` にする。権限とインストールの設定は[開発ガイド](development.md#github-app)に従う。
4. GitHub client secretとCookie暗号化鍵をSecret Managerへ登録し、実行用サービスアカウントに読み取りを許可する。テンプレートのSecret名は `github-client-secret` と `cookie-key`。Cookie鍵には `openssl rand -base64 32` の出力を使う。

GitHub user access tokenは暗号化Cookieに保持する。サービスアカウントの秘密鍵JSONや利用者のAIキーをコンテナへ配布しない。AIキーは画面から受け取る。

## コンテナのビルド

リポジトリルートをビルドコンテキストにし、`backend/Dockerfile` を使う。ビルド引数には次のFirebase公開設定を渡す。

| ビルド引数 | 内容 |
| --- | --- |
| `VITE_FIREBASE_PROJECT_ID` | FirebaseプロジェクトID |
| `VITE_FIREBASE_API_KEY` | WebアプリのFirebase APIキー |
| `VITE_FIREBASE_AUTH_DOMAIN` | Firebase Authドメイン |

`WEB_BUILD_MODE` は既定の `production` を使う。GitHub client secret、Cookie鍵、AIキーはビルド引数に渡さない。実行コンテナはnonrootで、`WEB_DIR=/app/web` からWebを配信する。

## Cloud Runの設定

`infra/cloud-run/service.yaml` の `REPLACE_*` を環境に合わせて置き換える。`WEB_ORIGIN` はcallbackと同じHTTPS Originにし、パスや末尾の `/` を付けない。

テンプレートは `PROVIDER=live`、OpenAI接続を使う。Gemini Developer APIを使う場合は `AI_PROVIDER=gemini` を設定し、必要に応じて `GEMINI_MODELS` と `GEMINI_REQUEST_TIMEOUT` を追加する。Anthropic APIは `AI_PROVIDER=anthropic`、Vertex AIは `AI_PROVIDER=vertex` と `VERTEX_PROJECT_ID` を設定する。複数プロバイダーも開発ガイドと同じ方法で併用できる。本番では `demo-` プロジェクト、Emulator、固定デモの設定を起動時に拒否する。

テンプレートのリクエスト期限は3,600秒、コンテナ同時実行数は16、最大インスタンス数は3。アプリ自身のWebSocket上限は1インスタンス4接続で、接続期限は最大55分である。これらは別々の設定なので、負荷に応じて確認する。

## Firestoreの設定

リポジトリルートからルールとインデックス設定を反映する。

```sh
firebase deploy --only firestore --project PROJECT_ID
```

[firestore.indexes.json](../firestore.indexes.json) に定義した `sessions`・`githubReadCache` の `ExpiresAt` のTTLが有効か確認する。ブラウザーからのFirestore直接読み書きはルールで拒否し、APIのサービスアカウントからアクセスする。

セッションのアクセス期限は作成から30日、読み取りキャッシュは1時間、生成内容のキャッシュは24時間。TTLによる物理削除は非同期であり、API側でも期限切れを拒否する。

## 配置後の確認

同一OriginでのWeb表示、GitHubログイン、許可したリポジトリの参照、選択したAIでの回答を確認する。停止・切断・再接続・手動再開、キー期限、他ユーザーからのアクセス拒否も確認する。

実装機能は計画承認と差分確認の後にDraft PRを作成し、対象コミットのCIを確認する。ブラウザーへ保存したAPIキーは実行用キーの期限切れやログアウトでは消えないため、保存済みキーの削除操作も確認する。
