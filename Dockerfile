FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -ldflags="-s -w" -o /out/radio ./cmd/radio

FROM alpine:3.22

RUN apk add --no-cache ffmpeg ca-certificates
WORKDIR /app
COPY --from=build /out/radio /usr/local/bin/radio

ENV ICECAST_URL=http://icecast:8000 \
    ICECAST_SOURCE_USER=source \
    HTTP_ADDR=:8080 \
    MAX_STATIONS=20 \
    FFMPEG_PATH=ffmpeg \
    FFPROBE_PATH=ffprobe \
    MINIO_ENDPOINT=minio:9000 \
    MINIO_SECURE=false \
    MEDIA_CACHE_DIR=/var/cache/radio-engine \
    REDIS_ADDR=redis:6379 \
    REDIS_STREAM=radio.events

RUN mkdir -p /var/cache/radio-engine

CMD ["radio"]
