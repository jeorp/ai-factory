# 設計と制限

現在の実装の構成、保存先、実行上限をまとめる。利用者の操作は[仕様書](product/specification.md)、接続設定とAPIは[開発ガイド](development.md)を参照する。

## コードの構成

| 場所 | 役割 |
| --- | --- |
| [frontend/web/src](../frontend/web/src/) | Vue、Pinia、Firebase認証、キー管理、WebSocket、結果表示 |
| [backend/cmd/api](../backend/cmd/api/) | 起動、設定、依存注入、HTTPクライアントの作成 |
| [backend/internal/httpapi](../backend/internal/httpapi/) | OAuth、Cookie、認証、HTTP、WebSocket、実行状態の制御 |
| [backend/internal/agent](../backend/internal/agent/) | チャット、レビュー、実装、資料作成、AI接続 |
| [backend/internal/github](../backend/internal/github/) | 権限確認、読み取り専用MCP、REST、キャッシュ、PR作成 |
| [backend/internal/store](../backend/internal/store/) | Firestore保存、接続lease、期限、削除 |
| [backend/internal/domain](../backend/internal/domain/) | 対象URL、履歴、計画、変更、受け入れ条件の検証 |

## 認証と接続

GitHub AppのOAuthはstateとPKCEを使う。GitHubの `/user` で確認したIDからFirebase UIDを生成し、同一OriginのHTTP応答でFirebaseカスタムトークンを返す。APIはFirebase ID tokenの失効を確認し、GitHub CookieのUIDと照合する。

GitHub user access tokenはAES-256-GCMで暗号化したHttpOnly・SameSite=Lax Cookieに保持する。本番はSecureを付ける。CookieのAADでOAuth state用とGitHub用の用途を分ける。有効期間はGitHubトークンの期限と8時間の早い方で、更新は再ログインで行う。refresh tokenは保存しない。

WebSocketはOriginと最初の認証フレームを検証する。接続は1ユーザーにつき1本、APIインスタンスにつき4本まで。Firestoreの45秒のleaseを10秒ごとに更新し、保存時にも同じleaseと期限をトランザクションで確認する。切断時は実行をキャンセルし、再接続後は利用者の再開操作を待つ。接続自体は最大55分で、GitHub Cookieの期限にも制限される。

チェックポイントの保存完了後に次の処理へ進む。停止要求後に届いた結果は採用しない。GitHubへの書き込みがすでに完了している場合、その操作は取り消さない。

## AIキーとデータの送信

実行用のAIキー・Vertex AIアクセストークンは [keyVault.ts](../frontend/web/src/lib/keyVault.ts) のメモリに保持する。入力から最大1時間で利用を停止し、セッション切替・終了・ログアウト・ページ終了でも参照を破棄する。サーバーは実行に必要な間だけキーを扱い、Firestoreや会話履歴には保存しない。

利用者が画面で保存を選んだキーは、プロバイダー別にブラウザーのIndexedDBへ保存する。このコピーには暗号化や1時間の自動削除を適用せず、保存済みキーの削除操作で消す。ログアウトや実行用キーの期限切れでは削除しない。旧localStorageの保存値があればIndexedDBへ移し、旧値を削除する。

認証情報らしい形式を含むチャット本文や変更内容を拒否し、その実行のキー・トークンが取得結果やAI応答に含まれる場合も拒否する。参照の破棄はメモリ上のバイトの即時ゼロ化や、プロバイダー側でのキー失効を保証しない。

取得したコード・PR・Issue・コメントなど、処理に必要な情報を選択したAIプロバイダーへ送る。OpenAIのリクエストには `store:false` を指定する。リポジトリ内の文章は参照データとして扱い、操作権限を決める指示にはしない。

## GitHubの読み取りとキャッシュ

モデルは許可された構造化要求を返し、サーバーがリポジトリ、コミット、操作名を決める。MCPには `/readonly` と `X-MCP-Readonly:true` を指定する。PRメタデータと差分はREST、関連コードとコメントはMCPで読む。実装用の元ファイル、コード検索、書き込みには専用のREST処理を使う。

通常チャットはセッションに保存したコミットを参照する。セッション作成時はデフォルトブランチのSHAを取得する。PRレビューでは開始時のbase/headを保存し、関連コードをheadから読む。レビュー開始後はセッションの参照コミットもheadへ更新する。実装は自身の基準コミットを保持する。

| 保存先 | 内容と期限 |
| --- | --- |
| 実行中のメモリ | 通常チャットの取得結果。同じ要求の重複取得を抑える |
| Firestore `githubReadCache` | コミット固定のファイル・ディレクトリ、レビュー差分。1時間 |
| Firestoreの変更内容キャッシュ | 生成したドラフト。24時間、利用者の操作で延長可能 |
| ブラウザーのIndexedDB | 表示・保存用のソースファイル。1時間、1ファイル100 KiB、全体10 MiB |

キャッシュの利用前にもGitHubのアクセス権を確認する。コミットに固定されないPR・Issue・コメントは通常の読み取りキャッシュに入れない。生成した変更内容はランダムな `CacheID` とバージョンで区別し、ブラウザーに `CacheID` を返さない。生成内容の期限切れは `implementation_changes_expired` で停止し、古いベースの内容へ戻さない。

ソースファイルはコミットごとのGitHubリンク、行番号、構文色分けとともに表示し、コピー・個別保存・一括保存できる。取得APIはセッション所有者と、セッションに記録したコミットを検証する。ファイル本文を会話履歴には追加しない。ブラウザーのファイルキャッシュはログアウト・セッション削除時にも消す。APIキーの保存領域とは別に管理する。

## 通常チャット

最初のAI判断で質問の目的・調査方針と読み取り要求をまとめて返す。直近の会話、要点、追加指示、取得結果を次の判断へ渡す。追加指示は新しいものから合計4,096バイトまで含め、省略を明示する。

各判断に `chatBudget` を渡す。最後の1判断は回答用に予約し、取得上限または最終判断ではスキーマの `read` を `null` に限定する。ファイル要求を使い切った場合は候補から `file` を除く。モデルが制約を守らなくても追加取得せず、保存済みの調査メモと未確認の案内で終える。

新しいセッションの最初の実行ではREADMEから短いリポジトリ概要を作り、後続のチャットとレビューの背景情報に使う。概要生成には通常チャットの判断とは別のAI呼び出しを使い、同じリポジトリ・コミットの概要は1時間再利用する。作成元のコミットと省略の有無を残し、レビュー指摘の証拠には使わない。概要を作れなくても実行を続ける。

## PRレビュー

通常メッセージの `startReview` で同じ会話のレビューへ移る。取得に成功したPR番号を `activePullRequest` に保存し、番号0は最後に取得したPRを指す。旧セッションでは保存済みのPR参照リンクも使う。

開始時のbase/headとファイル一覧を保存し、ディレクトリごとに関連する実装・テストを最大10ファイルでまとめる。差分が大きい場合は行境界で分割し、元の左右の行番号を引き継ぐ。処理位置はバッチ全体の保存後に進める。入力から省いた範囲、バイナリ、欠落した差分は未確認として残す。

「バックエンドのみ」では `frontend/` を、「フロントエンドのみ」では `backend/` を除外する。共通設定や文書は確認対象に残す。範囲の指定は再開時も保持する。

全体照合では要点と指摘をまとめ、必要な元コードを再取得する。複数ファイルにまたがる指摘は、その区切りで異なる2ファイル以上を取得し、パス・行を確認できた場合に採用する。未確認事項には優先度と理由を付け、追加調査の結果を会話とレビューに保存する。

取得・保存の前後でPRのbase/headと変更ファイル数を再確認する。更新を検出すると `pr_changed_create_new_review` で停止し、以前の結果をSHA付きで残す。再レビューの依頼で計画を作り直す。

## 実装と静的検証

処理は計画、承認、編集、静的検証、差分確認、GitHubへの書き込み、CI確認の順に進む。主な状態は次のとおり。

| `Implementation.Status` | 意味 |
| --- | --- |
| `planning` / `plan_ready` | 調査中／計画承認待ち |
| `editing` / `amend_ready` | 編集中／計画外ファイルの承認待ち |
| `verifying` / `verification_blocked` | 静的検証中／未解決の指摘や未確認事項あり |
| `ready` / `publishing` | 差分確認・書き込み待ち／書き込み中 |
| `checking_ci` / `published` | CI確認中／公開処理完了。CIの成否は別に保持 |

### 計画と編集

計画・編集・検証に同じ基準コミットのREADMEを `projectDocumentation` として渡す。コード検索はリポジトリとコミットを固定した文字列検索で、継続位置をcursorに持つ。計画前に変更・削除対象の実在と追加対象の不在を検証し、隣接テストや計画時に読んだファイルも一度見直す。

計画には受け入れ条件と確認方法を含める。`dependsOn` の依存先から編集し、循環する依存は同じ区切りで扱う。5ファイルを超える循環は再計画を求める。計画外の追加提案は `amend_ready` で止め、利用者が承認した後に加える。見送り済みのパスは再提案しない。

更新は検索・置換ブロック、新規追加は全文で受け取る。検索は一意な一致を必須とする。不一致・複数一致では `line_ranges` に切り替え、元ファイルのSHA-256、行範囲、両端の原文を照合して適用する。曖昧な一致には緩和しない。

編集の1区切りは5呼び出しで、取得は最大2回とし、変更生成と訂正のために3回分を確保する。入力が大きい場合は古い取得結果や優先度の低い関連コードから1件ずつ減らし、生成済みコードを優先する。

後続の編集や追加取得には生成済みの最新コードを優先し、削除済みなら削除を知らせる。公開前の追加依頼は生成内容を新しいキャッシュへコピーし、固定ベースに対する `create/update/delete` を保って再計画する。変更不要の保存済みファイルも差分に残し、保持だけでCI修正を完了扱いにしない。

応答形式の不正は最大2回自動再試行する。通信エラー、429、一部の5xxも各段階の予算内で最大2回再試行し、待機前に状態を保存する。Geminiの編集タイムアウトは依存関係を保って分割し、分割できない場合や他の段階では最大1回再試行する。利用者の停止・認証失敗・権限不足・保存失敗には適用しない。原文不一致への訂正と構造検証は[実装編集の検証と再試行](implementation-edit-reliability.md)を参照する。

### 検証と公開条件

全変更後コードと、条件の根拠・計画時の参照ファイル・隣接テストを確認する。大きいファイルは分割し、確認するコード本文を省略しない。各条件に `met/unmet/unverified` と根拠を返させ、未取得コードによる達成判定を拒否する。未確認事項を消すには追加のコード取得を必要とする。

根拠と修正候補のある指摘は、承認済みの範囲で最大2回自動修正する。`issues` は不具合、`unverified` は静的に確認できない点、`deferredChecks` は公開後のCIで実行する確認とする。指摘・静的な未確認・条件の未達や確認漏れが残ると `verification_blocked` で公開を止める。受け入れ条件のない旧計画は再計画を求める。

変更不要と判断したupdateは編集担当の提案だけでは計画から外さない。検証でも確認し、全条件と確認範囲を満たした場合に実効計画から外す。元の項目と理由は `planAdjustments` に残す。

Go、JavaScript/TypeScript、Pythonには埋め込みの検証観点を渡す。更新手順とルールは [verification_knowledge](../backend/internal/agent/verification_knowledge/README.md) を参照する。静的検証はAIによるコードの読み直しであり、対象リポジトリのビルド・テストはGitHub CIで実行する。

### GitHubへの書き込みとCI

書き込みは専用アダプターから行い、毎回push権限を確認する。Git Data APIでblob・tree・commitを作り、新しい `ai-factory/` ブランチからDraft PRを作成する。シンボリックリンク・サブモジュールを拒否し、既存ファイルの実行ビットを保持する。完了したcommit・ref・PR番号を段階ごとに保存し、再試行は残りだけを行う。同じheadのPRがあれば再利用する。

既存PRへの修正はオープン・同じリポジトリ・`ai-factory/` ブランチの条件を検証する。`ExpectedHead` と実際の先頭が一致する場合だけ `force:false` で追加し、他者の更新は `branch_moved` で停止する。

公開後は対象コミットのCIを10秒間隔で最大5分確認する。未完了なら実装を `checking_ci`、セッションを `ci_wait` に保存し、接続中のブラウザーが10秒後にAIキーなしで `resume` を送る。切断中は確認を止め、再接続後に続ける。`stop` は区切りの間の待機も止める。0件・スキップ・一覧省略・別コミットの結果を成功にせず、5分待っても0件なら未確認で終える。画面は待機状態と対象コミットのchecksリンクを表示する。再開はCI確認だけを行い、重複公開や完了メッセージの重複追加を避ける。

CI修正では失敗した全ジョブのログから失敗行と周辺行を渡し、計画の `failureRefs` に `ジョブID:L行番号` を対応させる。失敗情報は後続の取得で押し出さない。ログの不足、取得失敗、実行中のチェックがあればCI修正を止める。403はChecks/Actionsの読み取り権限、インストール先の権限更新、再ログインを案内する。

## 実装理解の資料

WebSocketの `documentation` コマンドで、`documentScope=feature`（機能・画面）または `repository`（全体）の資料を作成する。featureでは対象の入力を必須とし、全体資料は入力なしでも開始できる。通常チャットからの自動振り分けは行わず、画面上の「実装の資料」から開始する。

資料ごとに作成開始時のリポジトリとコミットを固定し、ルート一覧、関連するコード、テストを読み取り専用で調査する。AIは構造化した見出し・本文・参照パス・未確認事項を返す。サーバーは取得できたパスだけを参照リンクとして採用し、画面は本文をHTMLとして解釈しない。資料の目的、処理の流れ、データ・状態、変更時の注意点、読む順番をまとめ、Markdownへのコピー・保存に対応する。

調査は1実行最大10分・AIは再試行と再開を通じて合計24判断・20取得・15ファイル／ディレクトリ要求、1回の入力は96 KiB。出力は24,000バイト、8節まで。AI枠の最後の3回を最終生成と訂正に確保し、24回を使い切った資料は再開せず新規作成を案内する。呼び出し前に回数を予約して保存する。一時エラーは最大2回（Geminiのタイムアウトは1回）、形式不正は訂正文を1フィールドに置き最大2回まで再試行し、いずれも同じ24回の予算を消費する。最終生成中の指示は残り枠があれば反映し、残りがなければ完成した資料を保存して未反映であることを記す。時間切れは認証待ちではなく続行待ちとして表示する。読み取り枠を使い切った場合は最終資料の作成だけを許可する。取得結果は各12 KiBまでの抜粋とファイルごとの要点を保存し、取得結果全体をJSONエスケープ込みで64 KiBに制限する。保存前に古い本文から省略し、要点を優先して残す。これらの調査用本文はFirestoreに保存するがブラウザーへのセッションJSONには含めず、完成時に取り除く。入力上限による本文省略、取得失敗、未取得の参照は資料の制限に残す。

セッションの `Documentation` に最新の資料を保存する。資料作成が現在のワークフローである間は、停止・切断・一時エラー後に `resume` で保存済みの調査から再開できる。別の対象で新規作成すると前の資料を置き換える。保存期限と削除はセッションと共通であり、未完了の調査用本文にも同じ期限が適用される。

## 保存と削除

`users/{uid}/sessions/{id}` に会話、要点、参照元、固定対象、各ワークフローの状態、利用量、`ExpiresAt` を保存する。接続leaseは `users/{uid}/locks/connection` に置く。UIDは検証したトークンから採用し、ブラウザーからのFirestore直接読み書きは拒否する。

セッションは作成から30日後にアクセス不可となる。Firestore TTLによる物理削除は非同期で行う。本人による削除はサイドバーから行い、接続中なら切断・lease解放後に再試行する。生成した変更内容は24時間保持し、実装パネルから期限を延長できる。全ファイルが期限内にある場合だけ、セッションの有効期限を上限に、現在時刻から24時間後までメタデータを一括更新する。期限切れの内容は復活させず、本文やバージョンを上書きしない。

## モデルの振り分けと利用量

[ModelRouter](../backend/internal/agent/router.go) がモデルIDに対応するOpenAI、Gemini、Anthropic API、Vertex AIのアダプターを選ぶ。同じセッションの各段階は選択したモデルを使う。Geminiの推論量はセッションに保存し、対応値をサーバーで検証する。モデルごとの既定値は [DefaultEffort](../backend/internal/domain/effort.go) に従う。

段階別の呼び出し数、入出力トークン、応答時間を `metrics.phases` に保存する。セッションの累計トークン上限は0で無制限、最大100,000,000。実行停止中に変更でき、再開時も累計を引き継ぐ。上限到達後は次のAI呼び出し前に `ai_token_budget_exceeded` で止まり、計画・編集位置を保存する。公開とCI再確認はAIを使わないため、この予算停止の対象外とする。最後の1応答分は上限を超える場合があり、金額の上限は保証しない。

## 実行上限

| 対象 | 上限 |
| --- | --- |
| ユーザー本文・追加指示1件 | 各4,096バイト |
| 保存する会話 | 200メッセージ、本文合計300 KiB |
| GitHub取得1応答 | 100 KiB |
| 通常チャット1実行 | 3分、6取得、5ファイル要求、AI7判断 |
| 通常チャットの入力 | コンテキストJSON 24 KiB、APIリクエスト40 KiB |
| レビュー1実行 | 15分、AI100呼び出し、実利用入力50万・出力6.4万トークン |
| レビュー計画・入力 | 先頭200ファイル、1バッチ10ファイル・差分約2万トークン、入力全体約3万トークン・256 KiB |
| 資料作成 | 1実行10分、再試行・再開を通じAI24判断（最後の3回は最終生成・訂正用）、20取得・15ファイル要求 |
| 資料の入力・保存 | 入力JSON 96 KiB、保存する取得結果全体64 KiB、出力24,000バイト・8節 |
| 実装1実行 | 30分、AI100呼び出し、回答枠16,384トークン／呼び出し |
| 実装の変更セット | 20ファイル、1ファイル100 KiB、変更後本文の合計200 KiB |
| 実装の入力 | 約6万トークン、256 KiB |
| 外部HTTP | GitHub・OpenAIは60秒、Geminiは既定180秒 |

GeminiとClaudeのリクエストでは回答枠に思考用の8,192トークンを加える。Geminiの思考トークンも出力使用量に含める。

トークン見積もりには共通の `o200k_base` を使い、実利用量はプロバイダーのusageから集計する。レビューの利用量上限は次の呼び出し前に判定するため、最後の1呼び出し分だけ超える場合がある。料金表示は [pricing.ts](../frontend/web/src/lib/pricing.ts) の登録単価によるUSD推定額で、未登録モデルは推定しない。料金の上限を制御する機能はない。

画面のヒープ値は処理の区切りで観測したGoプロセス全体の `HeapAlloc` の最大値である。OSのRSS、瞬間ピーク、セッション単体の使用量とは異なる。

## 変更時の参照先

| 変更する内容 | 主な実装 |
| --- | --- |
| モデル・環境設定 | [config.go](../backend/internal/httpapi/config.go)、[main.go](../backend/cmd/api/main.go) |
| コマンド・中断・保存 | [websocket_conn.go](../backend/internal/httpapi/websocket_conn.go)、[firestore.go](../backend/internal/store/firestore.go) |
| チャットの取得予算 | [chat_budget.go](../backend/internal/agent/chat_budget.go)、[session.go](../backend/internal/domain/session.go) |
| レビューの予算・分割 | [review_budget.go](../backend/internal/agent/review_budget.go)、[review.go](../backend/internal/agent/review.go) |
| 実装理解の資料 | [documentation.go](../backend/internal/agent/documentation.go)、[documentation_retry.go](../backend/internal/agent/documentation_retry.go)、[documentation_memory.go](../backend/internal/agent/documentation_memory.go) |
| 実装計画・編集・検証 | [implement_plan.go](../backend/internal/agent/implement_plan.go)、[implement_edit.go](../backend/internal/agent/implement_edit.go)、[implement_verify.go](../backend/internal/agent/implement_verify.go) |
| 変更不要の照合・公開条件 | [implement_reconcile.go](../backend/internal/agent/implement_reconcile.go)、[implementation.go](../backend/internal/domain/implementation.go) |
| 書き込み・パス制約 | [write.go](../backend/internal/github/write.go)、[change.go](../backend/internal/domain/change.go) |
| 推論量・累計予算 | [effort.go](../backend/internal/domain/effort.go)、[usage.go](../backend/internal/agent/usage.go) |
| ドラフトの延長 | [changes.go](../backend/internal/httpapi/changes.go)、[change_cache.go](../backend/internal/github/change_cache.go) |
| CIログ・結果 | [job_log.go](../backend/internal/github/job_log.go)、[implement_ci_result.go](../backend/internal/agent/implement_ci_result.go) |
