# AI Factory

> **Want to use this code? Please contact the copyright holder first.** This public repository does not grant permission to copy, modify, redistribute, or use its code in another project. See the [code use terms](LICENSE.md) before using it.

AI Factory is a chat app for exploring GitHub repositories, reviewing pull requests in manageable batches, and turning requested changes into draft pull requests. The implementation workflow includes approval of the plan and review of the resulting diff.

| Feature | What it does |
| --- | --- |
| GitHub chat | Answers questions using repository, pull request, and issue information |
| Pull request review | Reviews diffs in batches and shows findings with evidence and areas that remain unverified |
| Implementation | Creates changes after plan approval and opens a draft pull request after diff review |

The stack uses Vue 3, TypeScript, Vue Router, Pinia, Go, Firebase Auth, Firestore, and Docker Compose, following `../system-design-skill-developer`. You can choose OpenAI or the Gemini Developer API. In production, the Go API serves the built Vue app from the same Cloud Run service so WebSocket connections and authentication cookies share one origin.

## Run locally

You need Docker, Go 1.27.2 or later, npm, and a supported Node.js LTS release (22.12 or later in the 22.x line, or 24.x). Run these commands from the repository root:

```sh
docker compose up --build -d --wait
cd frontend/web
npm ci
npm run dev
```

Open the [web app](http://127.0.0.1:5175/), use the demo login button, create a session, and send a question. The default mode is a **fixed demo**: it does not connect to GitHub or OpenAI and needs no API key. Firebase Emulator still handles authentication and stores the session. URLs shown as references in the demo are examples, not evidence of real code. The app's interface is currently in Japanese.

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
- OpenAI API keys stay in browser and server memory for at most one hour and are discarded when the session ends, the user logs out, or the page closes. Firestore data is separated by user, connection leases prevent duplicate runs, and records expire after 30 days. Users can also delete their data.

### Pull request review

After fetching a pull request in a repository conversation, ask “Review this PR” or specify a number, such as “Review PR #123.” The PR number is saved in the session and carried over from the source in existing conversations. If the target is unclear, the app asks for the PR number in chat.

- The app saves the base and head SHAs, a review plan in groups of up to 10 files, progress through the diff, findings, and unverified areas. Each finding shows severity, file and line, problem, impact, evidence, and a suggested fix. If the PR changes, review stops; a new request fetches its latest state.
- You can limit a review to “backend only” or “frontend only.” The corresponding `frontend/` or `backend/` diff is excluded from the AI input and marked out of scope in the plan. Shared configuration and documentation remain in scope, and the selection survives resumption.
- Findings are limited to issues that need a fix. The AI ranks unverified items as high, medium, low, or unevaluated, with reasons. Items it cannot assess remain visible; older reviews are marked unevaluated. Follow-up research is saved in both the conversation and review.

The review processes up to 10 related files and roughly 20,000 tokens per batch, targeting roughly 30,000 tokens for the full input. Token estimates use `o200k_base` and may differ from a model's actual count. A run continues for up to 15 minutes, 100 AI calls, 500,000 input tokens, and 64,000 output tokens, and plans the first 200 files. You can resume from saved progress after a limit, stop, or disconnect. Unreadable or binary files and input-limit omissions are reported as unverified. PR metadata and paginated diffs come from the GitHub REST API; related code and comments come from the read-only MCP connection. Ordinary chat runs have a three-minute limit.

### Implementation and draft pull requests

For an implementation request, the app presents a plan. After the user approves it, the agent creates changes. The user reviews the diff before the server opens a draft pull request on a new `ai-factory/` branch. Only dedicated server-side code writes to GitHub. The app does not run the target repository's code or tests; it checks GitHub CI after the PR is published. See the [development guide](docs/development.md) and [architecture and limits](docs/architecture.md) for details.

## Current limitations

Selecting or editing review findings, posting them to GitHub, checking the head at posting time, and preventing duplicate posts are not implemented. The app also does not include billing or credit management, Vertex AI, or execution of target repository code.

## Connect to live services

Live GitHub and AI access require a GitHub App owned by the user and an API key for the selected AI provider. Follow the [development guide](docs/development.md) for setup, [architecture and limits](docs/architecture.md) for the design, and [Cloud Run deployment guide](docs/deployment.md) for deployment. Credentials have not been registered and the app has not been deployed to the cloud.

To try the Gemini Developer API, set `AI_PROVIDER=gemini` in `.env` and restart with `docker compose up --build -d`. For example, set `GEMINI_MODELS=gemini-3.5-flash-lite,gemini-3.8-flash,gemini-3.1-pro-preview` to choose the available models (Pro has no free tier). Enter a Gemini API key from Google AI Studio in the app. The key is not stored in an environment variable; it is used temporarily for each session. Set `PROVIDER=demo` to keep GitHub in fixed-demo mode.

Gemini free-tier availability and limits depend on the Google project and model. A project with billing enabled may incur charges. On the free tier, submitted information may be used to improve Google's services, so check the terms before sending private code. This API is separate from Vertex AI.

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

This repository is public, but no license is granted to use its code in another project. Copying, modifying, redistributing, commercial use, and AI model training require prior written permission from the copyright holder, except for activities allowed by GitHub's Terms of Service or applicable law, such as viewing and forking. See [Copyright and use terms](LICENSE.md).
