FROM golang:1.25.4-alpine AS build
RUN apk add --no-cache git
WORKDIR /app
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/slack-codex-runner ./cmd/bot

FROM alpine:3.20
RUN apk add --no-cache git ca-certificates
WORKDIR /app
COPY --from=build /out/slack-codex-runner /usr/local/bin/slack-codex-runner
ENV ADDR=:8080 DATA_DIR=/data DB_PATH=/data/state.db WORKSPACE_ROOT=/data/workspaces
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/slack-codex-runner"]
