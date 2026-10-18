FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags='-s -w' -o /out/media-release-watcher ./cmd/media-release-watcher

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata wget && addgroup -g 10001 app && adduser -D -u 10001 -G app app && mkdir /data && chown app:app /data
COPY --from=build /out/media-release-watcher /usr/local/bin/media-release-watcher
USER 10001:10001
WORKDIR /data
ENV APP_ADDR=:8080 DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/media-release-watcher"]
