FROM node:22-trixie-slim
ARG FIREBASE_TOOLS_VERSION=15.30.1
RUN apt-get update && apt-get install --yes --no-install-recommends openjdk-21-jre-headless && rm -rf /var/lib/apt/lists/* && npm install --global "firebase-tools@${FIREBASE_TOOLS_VERSION}" && npm cache clean --force
ENV CI=true FIREBASE_CLI_DISABLE_UPDATE_CHECK=true GCLOUD_PROJECT=demo-ai-factory HOME=/home/node
WORKDIR /workspace
COPY firebase.json firestore.rules firestore.indexes.json ./
RUN mkdir -p /data /home/node/.cache/firebase/emulators && chown -R node:node /data /home/node/.cache /workspace
USER node
EXPOSE 4000 4400 8085 9099
CMD ["firebase", "emulators:start", "--project", "demo-ai-factory", "--only", "auth,firestore", "--import", "/data/export", "--export-on-exit", "/data/export"]
