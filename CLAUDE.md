# CLAUDE.md

## Проект

presureBot — Telegram-бот для учёта артериального давления. Пользователь фотографирует экран тонометра, бот распознаёт показания через OpenAI (vision, строгая JSON-схема), серию замеров сводит в одно значение и пишет в MongoDB (кластер Atlas, база `pressurebot`, коллекция `measurements`). По запросу рисует PNG-график.

## Структура

- `cmd/bot/` — точка входа, чтение env, цикл обновлений (каждое обновление в своей горутине)
- `internal/bot/` — обработчики Telegram; серии замеров хранятся в памяти (теряются при рестарте), автосохранение по `SESSION_TIMEOUT`
- `internal/pressure/` — `Reading`/`Measurement`, проверка диапазонов, `Average` (1–2 замера — среднее, ≥3 — первый отбрасывается), `Trend` (экспоненциально взвешенное по времени среднее, `TREND_HALF_LIFE`)
- `internal/recognize/` — клиент OpenAI Chat Completions (без SDK)
- `internal/storage/` — mongo-driver v2, индекс `(user_id, measured_at)` создаётся при старте
- `internal/chart/` — gonum/plot: красное верхнее, синее нижнее, пунктир — тренд, серые вертикали — границы суток (на периодах > 92 дней — границы месяцев)
- `internal/logging/` — маскирование секретов в логах (взято из GymBot)
- `deploy/` — compose и `.env.example` для `~/bots/presurebot/` на домашнем сервере (`slavco@10.100.102.39`); обновление — `~/bots/update-bot.sh presurebot`

## Команды

Go лежит в `~/sdk/go1.26.2/bin` (в неинтерактивных шеллах добавлять в PATH).

```bash
go test ./...                     # тесты; storage пропускается без TEST_MONGODB_URI
CHART_OUT=/tmp/chart.png go test ./internal/chart   # посмотреть пример графика
go build ./cmd/bot
```

Интеграционный тест хранилища:
```bash
docker run -d --rm --name mongo-test -p 57017:27017 mongo:7
TEST_MONGODB_URI='mongodb://localhost:57017' go test ./internal/storage
```

## Переменные окружения

Обязательные: `TELEGRAM_TOKEN`, `MONGODB_URI`, `OPENAI_API_KEY`. `ALLOWED_USER_IDS` (через запятую) — пока пуст, бот только сообщает отправителю его ID.
Необязательные: `MONGODB_DATABASE` (pressurebot), `OPENAI_MODEL` (gpt-4.1), `TZ` (Asia/Jerusalem), `TREND_HALF_LIFE` (72h), `SESSION_TIMEOUT` (20m).

## Соглашения

Комментарии и тексты для пользователя — на русском. Ошибки, которые могут содержать URL с токеном, пользователю не показывать — только в лог.
