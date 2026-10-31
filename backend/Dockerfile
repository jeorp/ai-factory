FROM node:24-trixie-slim AS web
WORKDIR /web
COPY frontend/web/package*.json ./
RUN npm ci
COPY frontend/web/ ./
ARG WEB_BUILD_MODE=production
ARG VITE_FIREBASE_PROJECT_ID
ARG VITE_FIREBASE_API_KEY
ARG VITE_FIREBASE_AUTH_DOMAIN
ENV VITE_FIREBASE_PROJECT_ID=$VITE_FIREBASE_PROJECT_ID VITE_FIREBASE_API_KEY=$VITE_FIREBASE_API_KEY VITE_FIREBASE_AUTH_DOMAIN=$VITE_FIREBASE_AUTH_DOMAIN
RUN if [ "$WEB_BUILD_MODE" = emulator ]; then npm run build:local; else npm run build; fi
FROM golang:1.27.2 AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /app/api
COPY --from=web /web/dist /app/web
ENV PORT=8080 WEB_DIR=/app/web
EXPOSE 8080
ENTRYPOINT ["/app/api"]
