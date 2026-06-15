# Radio Engine

Небольшой сервис онлайн-радио на Go. Он читает MP3-плейлисты, держит source-подключения к Icecast2 и может обслуживать несколько станций в одном процессе.

## Запуск

```bash
docker compose up --build
```

После запуска:

- stream одной станции: <http://localhost:8000/radio.mp3>
- Icecast2 status page: <http://localhost:8000/>
- command API: <http://localhost:8080/stations>

По умолчанию сервис берет все `.mp3` из `tracklist/` и играет их по кругу.

Для локального запуска без Docker:

```bash
go run ./cmd/radio
```

Чтобы запустить конкретный файл вместо директории:

```bash
TRACK_PATH="tracklist/The Ink Spots - Maybe.mp3" go run ./cmd/radio
```

## Несколько станций

Станции живут внутри одного Go-процесса: на каждую станцию создается горутина, плейлист и одно source-соединение к Icecast. Отдельные процессы под станции не запускаются.

Быстрый запуск 10 одинаково настроенных станций:

```bash
STATION_COUNT=10 go run ./cmd/radio
```

Через Docker Compose:

```bash
STATION_COUNT=10 docker compose up --build
```

Потоки будут доступны как:

- `http://localhost:8000/station-1.mp3`
- `http://localhost:8000/station-2.mp3`
- ...
- `http://localhost:8000/station-10.mp3`

Явный список станций:

```bash
STATIONS=main,news go run ./cmd/radio
```

Для переопределений используется префикс `STATION_<ID>_`, где все не буквы и не цифры заменяются на `_`, а буквы приводятся к верхнему регистру:

```bash
STATIONS=main,news \
STATION_MAIN_NAME="Main Radio" \
STATION_MAIN_MOUNT=/main.mp3 \
STATION_MAIN_PLAYLIST_DIR=tracklist/main \
STATION_NEWS_NAME="News Radio" \
STATION_NEWS_MOUNT=/news.mp3 \
STATION_NEWS_PLAYLIST_DIR=tracklist/news \
go run ./cmd/radio
```

## Команды

Команды вызываются через HTTP API. По умолчанию API слушает `:8080`; адрес меняется через `HTTP_ADDR`.

Посмотреть все станции:

```bash
curl http://localhost:8080/stations
```

Посмотреть состояние одной станции:

```bash
curl http://localhost:8080/stations/default
```

Пропустить текущий трек:

```bash
curl -X POST http://localhost:8080/stations/default/skip
```

Проиграть следующий announcement из `ANNOUNCEMENTS_DIR`:

```bash
curl -X POST http://localhost:8080/stations/default/announcement
```

Проиграть конкретный announcement-файл:

```bash
curl -X POST http://localhost:8080/stations/default/announcement \
  -H 'Content-Type: application/json' \
  -d '{"path":"tracklist/announcement.mp3"}'
```

Универсальная отправка команды:

```bash
curl -X POST http://localhost:8080/stations/default/commands \
  -H 'Content-Type: application/json' \
  -d '{"type":"skip_track"}'
```

Поддерживаемые `type`:

- `skip_track` - пропустить текущий трек;
- `play_announcement` - прервать текущий трек и проиграть announcement.

Если очередь команд станции переполнена, API вернет `503`.

## Переменные окружения

- `ICECAST_URL` default `http://127.0.0.1:8000`
- `ICECAST_SOURCE_USER` default `source`
- `ICECAST_SOURCE_PASSWORD` default `hackme`
- `ICECAST_MOUNT` default `/radio.mp3` для одной станции
- `STATION_ID` default `default`
- `STATION_NAME` default `Radio Engine <id>`
- `PLAYLIST_DIR` default `tracklist`
- `TRACK_PATH` optional single MP3 file instead of playlist directory
- `ANNOUNCEMENTS_DIR` optional directory with announcement MP3 files
- `RECONNECT_DELAY_SECONDS` default `3`
- `HTTP_ADDR` default `:8080`
- `STATIONS` optional comma-separated station ids, for example `main,news`
- `STATION_COUNT` optional generated station count, for example `10`
- `STATION_<ID>_NAME`
- `STATION_<ID>_MOUNT`
- `STATION_<ID>_PLAYLIST_DIR`
- `STATION_<ID>_TRACK_PATH`
- `STATION_<ID>_ANNOUNCEMENTS_DIR`
- `STATION_<ID>_DESCRIPTION`
- `STATION_<ID>_GENRE`
- `STATION_<ID>_RECONNECT_DELAY_SECONDS`

## Поток

MP3 отправляется по frame pacing: сервис читает MP3-фреймы, вычисляет длительность каждого фрейма из заголовка и пишет в Icecast с правильной скоростью для CBR и VBR.

Чтобы браузер не заикался после нескольких минут и на границах треков, стример держит небольшой стартовый запас аудио и по умолчанию не отправляет ID3v2-теги из файлов в live-поток.

## Архитектура

Главная точка входа: [cmd/radio/main.go](cmd/radio/main.go).

Пакеты:

- `internal/api` - HTTP API для состояния и команд;
- `internal/config` - конфигурация из переменных окружения;
- `internal/icecast` - source-клиент Icecast;
- `internal/mp3stream` - MP3 frame pacing и разбор заголовков;
- `internal/playlist` - загрузка MP3 из директории;
- `internal/radio` - менеджер нескольких станций;
- `internal/station` - playback loop, состояние и команды станции.