FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN go build -o /out/radio ./cmd/radio

FROM alpine:3.20

WORKDIR /app
COPY --from=build /out/radio /usr/local/bin/radio
COPY tracklist ./tracklist

ENV ICECAST_URL=http://icecast:8000 \
    ICECAST_SOURCE_USER=source \
    ICECAST_SOURCE_PASSWORD=hackme \
    ICECAST_MOUNT=/radio.mp3 \
    STATION_ID=default \
    HTTP_ADDR=:8080 \
    PLAYLIST_DIR=tracklist

CMD ["radio"]
