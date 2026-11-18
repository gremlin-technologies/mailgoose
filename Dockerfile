# syntax=docker/dockerfile:1

# One image runs both ECS services. The entrypoint is mailgoosed; ECS passes
# ["serve"] for the API and ["worker"] for the worker (infra/modules/stack).
# Fargate tasks run on ARM64, so build with --platform linux/arm64. Go
# cross-compiles on the build machine's own platform, so no emulation is needed.

ARG GO_IMAGE=golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath \
      -ldflags "-s -w -X github.com/gremlin-technologies/mailgoose/internal/version.Version=${VERSION} -X github.com/gremlin-technologies/mailgoose/internal/version.Commit=${COMMIT}" \
      -o /out/mailgoosed ./cmd/mailgoosed

# distroless/static: CA certificates and tzdata, no shell, runs as uid 65532.
FROM ${RUNTIME_IMAGE}
COPY --from=build /out/mailgoosed /usr/local/bin/mailgoosed
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mailgoosed"]
CMD ["serve"]
