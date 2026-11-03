# AI Factory

> **Commercial use of this code is prohibited.** For any other use beyond viewing and forking on GitHub, contact the copyright holder first. This public repository does not grant permission to copy, modify, redistribute, or use its code in another project. See the [code use terms](LICENSE.md).

AI Factory is a chat app for exploring GitHub repositories, reviewing pull requests in manageable batches, and turning requested changes into draft pull requests. The implementation workflow includes approval of the plan and review of the resulting diff.

| Feature | What it does |
| --- | --- |
| GitHub chat | Answers questions using repository, pull request, and issue information |
| Pull request review | Reviews diffs in batches and shows findings with evidence and areas that remain unverified |
| Implementation | Creates changes after plan approval and opens a draft pull request after diff review |
| Implementation guides | Creates saved guides for a feature, screen, or the whole repository, with processing flows and links to the code |

The frontend uses Vue 3, TypeScript, Vue Router, and Pinia. The backend uses Go, with Firebase Auth for authentication and Firestore for storage. Docker Compose runs the API and Firebase emulators locally. The server can offer OpenAI, Gemini Developer API, Claude API, and Claude through Vertex AI. Models are routed to their configured provider; see the development guide for credentials and setup. In production, the Go API serves the built Vue app from the same Cloud Run service so WebSocket connections and authentication cookies share one origin.

## Run locally

You need Docker with Docker Compose, npm, and a supported Node.js LTS release (22.12 or later in the 22.x line, or 24.x). Go is built inside Docker; install Go 1.27.2 or later on your machine only if you want to run the API or Go checks directly.

The default demo needs no `.env` file, GitHub App, AI API key, or Firebase cloud project. Start Docker, then run these commands from the repository root:

```sh
docker compose up --build -d --wait
cd frontend/web
npm ci
npm run dev
```

The app's interface is currently in Japanese. To try it:

1. Open the [web app](http://127.0.0.1:5175/) and click **開発用ログイン** (demo login).
2. Select the sample repository and click **セッションを作成** (create session).
3. Send a question, such as `このリポジトリの構成を教えて` (explain the repository structure).

The default mode is a **fixed demo**: it returns sample responses without connecting to GitHub or an AI provider. Firebase Emulator handles authentication and stores the session locally. URLs shown as references in the demo are examples, not evidence of real code.

| Service | URL |
| --- | --- |
| Web app | http://127.0.0.1:5175 |
| API | http://127.0.0.1:8181 |
| Emulator UI | http://127.0.0.1:4101 |
| Firebase Auth / Firestore | 127.0.0.1:9197 / 127.0.0.1:8186 |

To stop, press `Ctrl+C` in the web terminal and run `docker compose down` from the repository root. Emulator data lives in a dedicated volume, with ports and data separated from other projects.

## Features

### GitHub chat and access control

- GitHub App OAuth (state and PKCE) and Firebase custom tokens authenticate users. You can list repositories allowed by the GitHub App and create sessions from repository, pull request, or issue URLs.
- The GitHub MCP connection is read-only. The server pins the target repository, commit, and permitted operations. With OpenAI, the app uses structured output from the Responses API to fetch information and answer in stages.
- WebSocket supports questions, follow-up instructions, stopping, and progress updates. A disconnect interrupts the run; after reconnecting, you can restore the conversation and resume manually.
- The model receives recent conversation turns and a summary. Retrieved results are kept separate from saved history, and duplicate reads within a run are suppressed. The UI shows read counts, byte counts, input and output tokens, elapsed time, and observed process heap usage.
- Active AI API keys and Vertex AI access tokens stay in memory for at most one hour and are discarded when the session ends, the user logs out, or the page closes. If the user opts to save a key, a separate copy remains in the browser's IndexedDB until explicitly deleted; it is stored without encryption and survives logout. Firestore sessions are separated by user, connection leases prevent duplicate runs, and sessions expire after 30 days. Users can also delete sessions.

### Implementation guides

Open **実装の資料** (implementation guide) in a repository session. Choose **機能・画面** (feature or screen), enter what you want to understand, and select **資料を作成** (create guide). You can also choose **リポジトリ全体** (whole repository).

The agent reads code at the session's fixed commit and creates a guide covering the purpose, entry points, processing flow, data and state, change impacts, and suggested reading order. Each section links to retrieved code, and the guide keeps unverified areas visible. Guides are saved with the conversation and can be copied or downloaded as Markdown. The fixed demo produces an explicitly labeled sample, without inspecting real code.

Research is limited to 10 minutes per run, 24 AI calls across retries and resumptions, 20 reads, and 15 file/directory requests. Three of the AI calls are reserved for final generation and corrections. Transient errors and invalid responses can retry within the same call budget; an exhausted guide must be recreated. Intermediate research, including per-file notes, is checkpointed so an interrupted guide can resume while it remains the session's active workflow. Stored evidence is capped at 64 KiB, with omitted excerpts reported in the guide. The guide describes static code inspection; it does not execute repository code or tests.

### Pull request review

After fetching a pull request in a repository conversation, ask “Review this PR” or specify a number, such as “Review PR #123.” The PR number is saved in the session and carried over from the source in existing conversations. If the target is unclear, the app asks for the PR number in chat.

- The app saves the base and head SHAs, a review plan in groups of up to 10 files, progress through the diff, findings, and unverified areas. Each finding shows severity, file and line, problem, impact, evidence, and a suggested fix. If the PR changes, review stops; a new request fetches its latest state.
- You can limit a review to “backend only” or “frontend only.” The corresponding `frontend/` or `backend/` diff is excluded from the AI input and marked out of scope in the plan. Shared configuration and documentation remain in scope, and the selection survives resumption.
- Findings are limited to issues that need a fix. The AI ranks unverified items as high, medium, low, or unevaluated, with reasons. Items it cannot assess remain visible; older reviews are marked unevaluated. Follow-up research is saved in both the conversation and review.

The review processes up to 10 related files and roughly 20,000 tokens per batch, targeting roughly 30,000 tokens for the full input. Token estimates use `o200k_base` and may differ from a model's actual count. A run continues for up to 15 minutes, 100 AI calls, 500,000 input tokens, and 64,000 output tokens, and plans the first 200 files. You can resume from saved progress after a limit, stop, or disconnect. Unreadable or binary files and input-limit omissions are reported as unverified. PR metadata and paginated diffs come from the GitHub REST API; related code and comments come from the read-only MCP connection. Ordinary chat runs have a three-minute limit.

### Implementation and draft pull requests

For an implementation request, the app presents a plan. After the user approves it, the agent creates changes. The user reviews the diff before the server opens a draft pull request on a new `ai-factory/` branch. Only dedicated server-side code writes to GitHub. The app does not run the target repository's code or tests; it checks GitHub CI after the PR is published. See the [development guide](docs/development.md) and [architecture and limits](docs/architecture.md) for details.

## Connect to live services

To use your own repositories and receive live AI responses, configure a GitHub App you own and an API key for the selected AI provider. Set both `PROVIDER=live` and `AI_PROVIDER=openai`, `gemini`, `anthropic`, or `vertex` in `.env`, then restart the API.

To use a live AI provider with sample GitHub data, keep `PROVIDER=demo` and set `AI_PROVIDER=openai`, `gemini`, `anthropic`, or `vertex`. Enter the provider's API key in the app. Model choices come from `OPENAI_MODELS`, `GEMINI_MODELS`, `ANTHROPIC_MODELS`, or `CLAUDE_MODELS` in the server configuration.

| Documentation | Contents |
| --- | --- |
| [Documentation index](docs/README.md) | Guides for users and contributors |
| [Product specification](docs/product/specification.md) | Current features and user workflows |
| [Development guide](docs/development.md) | Live-service setup, API, checks, and logs |
| [Architecture and limits](docs/architecture.md) | Storage, resumption, approval rules, budgets, and source-code references |
| [Cloud Run deployment](docs/deployment.md) | Container, authentication, secrets, and Firestore configuration |

Documentation is in Japanese. Check the provider's current service terms before sending private code. Cost estimates use the rates registered in the app and do not impose a spending limit.

## Verify

```sh
cd backend
go test -race ./...
go vet ./...
# First run docker compose up -d --wait firebase-emulator from the repository root.
RUN_EMULATOR_TESTS=1 FIREBASE_AUTH_EMULATOR_HOST=127.0.0.1:9197 FIRESTORE_EMULATOR_HOST=127.0.0.1:8186 go test -race ./internal/httpapi -run TestEmulator -v
cd ../frontend/web
npm test
npm run build:local
```

Regular tests replace external connections with stubs. Integration tests use Firebase Auth and Firestore Emulator with a real WebSocket connection. End-to-end testing against a real GitHub App, GitHub MCP, and OpenAI must be run separately in an environment with credentials.

## Code use terms

**Commercial use of this repository's code is prohibited.** No license is granted to use the code in another project. Other uses, including copying, modifying, redistributing, and AI model training, require prior written permission from the copyright holder. Activities allowed by GitHub's Terms of Service or applicable law, such as viewing and forking, are unaffected. See [Copyright and use terms](LICENSE.md).
