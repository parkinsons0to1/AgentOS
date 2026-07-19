# AgentOS base image: a headless agent environment. No display stack, no
# window manager — the only interfaces are the shell, the gateway, and files.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/agentd ./cmd/agentd

FROM alpine:3.21
RUN apk add --no-cache ca-certificates bash curl jq git python3 \
    && adduser -D -h /home/agent agent \
    && mkdir -p /workspace && chown agent:agent /workspace
COPY --from=build /out/agentd /usr/local/bin/agentd
USER agent
WORKDIR /workspace
ENTRYPOINT ["agentd"]
CMD ["version"]
