# Radio Engine

proof of concept: Небольшой сервис, который стримит треки из `tracklist/`
в Icecast.

## Запуск

```bash
docker compose up --build
```

Ссылки:

- stream: <http://localhost:8000/radio.mp3>
- Icecast status page: <http://localhost:8000/>

Берёт первый `.mp3` из `tracklist/` по дефолту. Так же можно выбрать конкретный с помощью:

```bash
TRACK_PATH="tracklist/The Ink Spots - Maybe.mp3" go run ./cmd/radio
```

Остальные переменные окружения:

- `ICECAST_URL` default `http://127.0.0.1:8000`
- `ICECAST_SOURCE_USER` default `source`
- `ICECAST_SOURCE_PASSWORD` default `hackme`
- `ICECAST_MOUNT` default `/radio.mp3`
- `STREAM_BITRATE_KBPS` default `128`
