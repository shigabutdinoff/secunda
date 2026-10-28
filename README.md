# Secunda

Сервис ведёт задачи команд по REST API. В команду приглашают участников, задачам
меняют статус и исполнителя, к ним пишут комментарии, каждое изменение остаётся
в истории. Владелец и администраторы смотрят статистику команды.

Данные лежат в MySQL, списки задач кешируются в Redis.

## Запуск

```sh
cp .env.example .env
docker compose up -d --build   # http://localhost:8080
docker compose down -v         # остановка с удалением данных
```

## Конфигурация

| Переменная        | Флаг | По умолчанию     |
|-------------------|------|------------------|
| `ADDRESS`         | `-a` | `:8080`          |
| `DATABASE_DSN`    | `-d` | обязательна, с `parseTime=true` |
| `JWT_SECRET`      | `-j` | обязательна      |
| `REDIS_ADDR`      | `-r` | `localhost:6379` |
| `MIGRATIONS_PATH` | `-m` | `migrations`     |

Переменная важнее флага. Compose берёт значения из `.env`.

## Миграции

Применяются при старте сервиса (golang-migrate). Откат последней:

```sh
set -a; . ./.env; set +a
docker run --rm -v "$PWD/migrations:/migrations" --network "$(basename "$PWD")_default" \
  migrate/migrate -path /migrations \
  -database "mysql://$MYSQL_USER:$MYSQL_PASSWORD@tcp(mysql:3306)/$MYSQL_DATABASE" down 1
```

## Пример: от регистрации до статистики

Нужны `curl` и `jq`, сервис поднят через `docker compose up`.

```sh
API=http://localhost:8080/api/v1
json='Content-Type: application/json'

curl -s -H "$json" -d '{"email":"owner@example.com","password":"password1","name":"Owner"}' $API/register
curl -s -H "$json" -d '{"email":"dev@example.com","password":"password1","name":"Dev"}' $API/register

TOKEN=$(curl -s -H "$json" -d '{"email":"owner@example.com","password":"password1"}' $API/login | jq -r .token)
auth="Authorization: Bearer $TOKEN"

TEAM=$(curl -s -H "$auth" -H "$json" -d '{"name":"Backend"}' $API/teams | jq -r .id)
DEV=$(curl -s -H "$auth" -H "$json" -d '{"email":"dev@example.com","role":"member"}' $API/teams/$TEAM/invite | jq -r .user_id)
curl -s -X PATCH -H "$auth" -H "$json" -d '{"role":"admin"}' $API/teams/$TEAM/members/$DEV

TASK=$(curl -s -H "$auth" -H "$json" -d "{\"team_id\":$TEAM,\"title\":\"Первая задача\",\"description\":\"\"}" $API/tasks | jq -r .id)
curl -s -X PUT -H "$auth" -H "$json" -d '{"version":1,"status":"done"}' $API/tasks/$TASK
curl -s -H "$auth" $API/teams/$TEAM/stats
```

## Тесты

```sh
go test ./...
```

Интеграционным тестам нужен Docker. Тест статистики сам поднимает MySQL через testcontainers, остальные без этих переменных пропускаются:

```sh
docker run -d --name secunda-test-mysql -p 3307:3306 \
  -e MYSQL_ROOT_PASSWORD=test -e MYSQL_DATABASE=secunda_test mysql:8.4
docker run -d --name secunda-test-redis -p 6380:6379 redis:8-alpine

TEST_DATABASE_DSN='root:test@tcp(localhost:3307)/secunda_test?parseTime=true' \
TEST_REDIS_ADDR=localhost:6380 go test ./...
docker rm -f secunda-test-mysql secunda-test-redis
```
