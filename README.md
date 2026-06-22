# Radio Engine

Stateless Go media-worker для онлайн-радио. Бизнес-бек владеет станциями и полной очередью, а этот сервис держит короткое runtime-окно, читает media из MinIO/S3, микширует PCM-аудио через FFmpeg, отдаёт MP3 source-потоки в Icecast и публикует события в Redis Streams.

## Быстрый запуск

```bash
docker compose up --build
```

После запуска:

- API: <http://localhost:8080/v1/stations>
- health: <http://localhost:8080/health/ready>
- metrics: <http://localhost:8080/metrics>
- Icecast: <http://localhost:8000/>
- MinIO console: <http://localhost:9001/>

Сервис стартует без станций. Backend должен создать станции заново после рестарта worker-а.

## API v1

Создать или обновить станцию со стабильным backend ID:

```bash
curl -X PUT http://localhost:8080/v1/stations/main \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Main Radio",
    "description": "Main stream",
    "genre": "Various",
    "output": { "bitrateKbps": 192 },
    "transitionDefaults": { "type": "crossfade", "durationMs": 2500 },
    "announcementProfile": {
      "musicGainDb": -12,
      "announcementGainDb": 0,
      "attackMs": 180,
      "releaseMs": 450,
      "mid": { "enabled": true, "gainDb": -18 },
      "high": { "enabled": true, "gainDb": -14 },
      "limiterThresholdDb": -1
    }
  }'
```

Заменить пять следующих треков:

```bash
curl -X PUT http://localhost:8080/v1/stations/main/queue \
  -H 'Content-Type: application/json' \
  -d '{
    "revision": 1,
    "tracks": [
      {
        "itemId": "track-001",
        "media": { "bucket": "music", "objectKey": "tracks/track-001.flac" },
        "title": "Track 001",
        "transitionIn": { "type": "crossfade", "durationMs": 3000 }
      }
    ]
  }'
```

Правила очереди:

- engine хранит текущий трек отдельно и максимум 5 следующих;
- `revision` должен монотонно расти, иначе API вернёт `409 stale_revision`;
- замена очереди не прерывает текущий трек;
- `transitionIn` описывает переход от предыдущего трека к этому;
- если media недоступно или не декодируется, engine публикует failure event и переходит дальше.

Поставить оповещение в FIFO:

```bash
curl -X POST http://localhost:8080/v1/stations/main/announcements \
  -H 'Content-Type: application/json' \
  -d '{
    "commandId": "alert-42",
    "media": { "bucket": "alerts", "objectKey": "weather.wav" },
    "title": "Weather alert"
  }'
```

Повторный `commandId` идемпотентен. Оповещения накладываются поверх музыки с профилем станции; текущее оповещение не прерывается новым.

Команды:

```bash
curl -X POST http://localhost:8080/v1/stations/main/stop
curl -X POST http://localhost:8080/v1/stations/main/play
curl -X POST http://localhost:8080/v1/stations/main/skip
curl http://localhost:8080/v1/stations/main
curl -X DELETE http://localhost:8080/v1/stations/main
```

`stop` останавливает музыкальную очередь, но не блокирует внеплановые оповещения.

## Архитектура аудио

Для каждой активной станции создаётся один realtime-loop:

1. MinIO/S3 object скачивается в ограниченный disk cache.
2. FFmpeg декодирует media в PCM `48 kHz stereo f32le`.
3. Go mixer применяет переходы, ducking/EQ bands для оповещений и limiter.
4. Постоянный FFmpeg encoder кодирует PCM в MP3.
5. MP3 поток пишется в Icecast source connection.

Idle-станция продолжает отдавать тишину, чтобы mount не исчезал у клиентов.

## Redis события

События пишутся в Redis Stream `radio.events` с envelope:

- `eventId`
- `type`
- `stationId`
- `instanceId`
- `sequence`
- `timestamp`
- `correlationId`
- `payload`

Доставка в Redis — at-least-once; backend дедуплицирует по `eventId`.

## Переменные окружения

- `HTTP_ADDR`, default `:8080`
- `HTTP_BEARER_TOKEN`, optional
- `INSTANCE_ID`, default hostname
- `MAX_STATIONS`, default `20`
- `ICECAST_URL`, default `http://127.0.0.1:8000`
- `ICECAST_SOURCE_USER`, default `source`
- `ICECAST_SOURCE_PASSWORD`, default `hackme`
- `MINIO_ENDPOINT`, default `127.0.0.1:9000`
- `MINIO_ACCESS_KEY`, default `minioadmin`
- `MINIO_SECRET_KEY`, default `minioadmin`
- `MINIO_SECURE`, default `false`
- `MINIO_RETRIES`, default `3`
- `MEDIA_CACHE_DIR`, default `/tmp/radio-engine-cache`
- `MEDIA_CACHE_MAX_BYTES`, default `5368709120`
- `REDIS_ADDR`, default `127.0.0.1:6379`
- `REDIS_PASSWORD`, optional
- `REDIS_DB`, default `0`
- `REDIS_STREAM`, default `radio.events`
- `FFMPEG_PATH`, default `ffmpeg`
- `FFPROBE_PATH`, default `ffprobe`

Горизонтальный масштаб делается несколькими worker-репликами. Backend выбирает worker, создаёт станции через `PUT /v1/stations/{stationId}` и повторно применяет конфигурацию/очередь после рестарта.
