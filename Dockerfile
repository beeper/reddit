FROM golang:1-alpine3.23 AS builder

RUN apk add --no-cache git ca-certificates build-base su-exec olm-dev

ARG CI_COMMIT_SHA=unknown
ARG CI_COMMIT_TAG=
COPY . /build
WORKDIR /build
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build CI=true ./build.sh

FROM alpine:3.23

ENV UID=1337 \
    GID=1337

RUN apk add --no-cache ffmpeg su-exec ca-certificates olm bash jq yq curl

COPY --from=builder /build/reddit /usr/bin/reddit
COPY --from=builder /build/docker-run.sh /docker-run.sh
ARG CI_COMMIT_SHA=unknown
LABEL org.opencontainers.image.source="https://github.com/beeper/reddit" \
    org.opencontainers.image.revision="${CI_COMMIT_SHA}"
VOLUME /data
WORKDIR /data
ENTRYPOINT ["/docker-run.sh"]
