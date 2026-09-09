FROM node:24-alpine@sha256:e67514e5d0f6c46656005e1b693b2ec9d52e80b641307de684d4a015ba7a4eaf AS dashboard
WORKDIR /src/dashboard
RUN npm install --global pnpm@11.22.0
COPY dashboard/package.json dashboard/pnpm-lock.yaml dashboard/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY dashboard/ ./
RUN pnpm build

FROM golang:1.27-alpine@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS builder
WORKDIR /src
RUN apk add --no-cache python3
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ ./internal/
COPY cmd/cpractl/ ./cmd/cpractl/
COPY main.go LICENSE ./
RUN rm -rf internal/web/server/assets
COPY --from=dashboard /src/dashboard/dist/ ./internal/web/server/assets/
COPY scripts/release/ ./scripts/release/
COPY --from=dashboard /src/dashboard/package.json ./dashboard/package.json
COPY --from=dashboard /src/dashboard/node_modules/ ./dashboard/node_modules/
ARG BUILD_TAGS=""
ARG VERSION="dev"
ARG COMMIT="unknown"
ARG BUILD_DATE="unknown"
RUN python3 scripts/release/package.py --version container --tags "$BUILD_TAGS" --notices-only --out /out/licenses
RUN CGO_ENABLED=0 go build -trimpath -tags "$BUILD_TAGS" -ldflags="-s -w -X cpra/internal/version.Version=$VERSION -X cpra/internal/version.Commit=$COMMIT -X cpra/internal/version.Date=$BUILD_DATE" -o /out/cpra . && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X cpra/internal/version.Version=$VERSION -X cpra/internal/version.Commit=$COMMIT -X cpra/internal/version.Date=$BUILD_DATE" -o /out/cpractl ./cmd/cpractl

FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache ca-certificates && addgroup -g 1001 -S cpra && adduser -u 1001 -S cpra -G cpra
WORKDIR /app
COPY --from=builder /out/cpra /out/cpractl /usr/local/bin/
COPY LICENSE /usr/share/licenses/cpra/LICENSE
COPY --from=builder /out/licenses/ /usr/share/licenses/cpra/
RUN chown cpra:cpra /app
USER cpra
ENV GOMEMLIMIT=768MiB
EXPOSE 8060
ENTRYPOINT ["cpra"]
CMD ["-yaml", "/etc/cpra/monitors.yaml"]
