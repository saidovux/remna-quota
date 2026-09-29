# Разработка

Требуется Go 1.27.x. Новый процесс — `go run ./cmd/quota-api`; параметры берутся из окружения и `.env` (окружение имеет приоритет). Пример — `.env.example`. `keygen` генерирует ключ, `check-config` проверяет настройки без подключения к панели/базе и не выводит секреты.

`check-provider` выполняет только чтение API Remnawave. Проверяет metadata, by-id и connection keys; во включённом по умолчанию managed-режиме также профили сквадов и by-username. Команда не доказывает применение квоты; формат результата описан в [развёртывании](deployment.md).

В деморежиме используется память. В режиме Remnawave используется отдельная PostgreSQL и миграции из `migrations`. Рабочий процесс не подключается к Bedolaga.

```bash
go test ./...
go vet ./...
go build ./...
gofmt -w cmd internal migrations pkg
```

Для реальной проверки хранилища задайте `BACKEND_TEST_DATABASE_URL` на выделенную тестовую PostgreSQL и выполните `go test ./internal/bundlestore -v`. Тесты применяют миграции, создают записи с уникальными ID и удаляют только свои записи. Проверяются managed accounts и subscription bundles, атомарность проверки revision между репликами, пагинация, откат, ротация и удаление. Не используйте рабочую базу. CI поднимает PostgreSQL и запускает все тесты с `-race`, затем собирает контейнер.

## Явная проверка действующей панели

Дополнительный тест `TestLiveReadOnlyAggregation` не создаёт и не меняет пользователей панели. Для запуска задайте обычные параметры чтения Remnawave, `REMNA_LIVE_READ_ONLY=true` и `REMNA_LIVE_REFERENCES_JSON='{"main":"<existing-id>","cdn":"<another-existing-id>"}'`. Нужны две доступные подписки с выдаваемыми URI и права `system:metadata`, `users:by-id`, `subscriptions:connection-keys`. Выполните `go test ./internal/providers -run '^TestLiveReadOnlyAggregation$' -count=1 -v`.

Тест создаёт набор только в памяти, проверяет чтение, общий список, отсоединение CDN, resolver и удаление локального набора. HTTP transport дополнительно блокирует любые запросы к панели, кроме GET. Нативные ссылки и содержимое URI в вывод не попадают. Тест по умолчанию пропускается.

### Проверка создания и синхронизации

`TestLiveRemnawaveBundleLifecycle` по умолчанию пропускается. Он создаёт и удаляет два временных пользователя: MAIN без лимита, CDN с малой квотой и сроком в несколько часов. Проверяет SDK/HTTP API, повторную выдачу, общий список, независимое отключение/включение, изменение квоты и срока, resolver и смену токена. VPN-трафик не генерирует и базу сервиса не использует.

Для запуска нужны обычные `REMNAWAVE_BASE_URL`, `REMNAWAVE_API_TOKEN`, `REMNAWAVE_PROFILES_JSON` с `main` и `cdn`, при необходимости параметры HTTP/прокси. Временный токен должен иметь обычные права адаптера плюс `users:delete` для уборки. Тест не читает `.env` автоматически. Создавайте отдельный краткосрочный токен и отзывайте его после проверки.

```bash
export REMNA_LIVE_TEST=create-and-delete-test-users
export REMNA_LIVE_RUN_ID="$(openssl rand -hex 11)"
go test ./internal/providers -run '^TestLiveRemnawaveBundleLifecycle$' -count=1 -v
```

При нормальном завершении тест удаляет только пользователей с точным именем и маркером текущего запуска. При принудительном завершении процесса уборка может не выполниться: проверьте `rqlive_<REMNA_LIVE_RUN_ID>_main` и `..._cdn`, сверяя description `remna-quota:live-<REMNA_LIVE_RUN_ID>:<key>` перед удалением. Никакие существующие записи не должны подхватываться по одному имени. Ключи и URI в вывод теста не включаются.

Разделение кода:

- `internal/aggregate`: дополнительный набор существующих источников, атомарные изменения, revision и интерфейс провайдера только для чтения.
- `internal/bundlestore`: PostgreSQL, межпроцессные блокировки, список с курсором и сохранение снимков.
- `internal/providers`: адаптеры управления и чтения Remnawave v3, demo.
- `internal/subscription`: проверка URI, метки, Base64/plain экспорт.
- `internal/httpapi`: аутентификация, HTTP-контракты accounts/bundles и отдельные показатели частей.
- `internal/backend`, `cmd/quota-api`: конфигурация, сервер и фоновая синхронизация.
- `pkg/client`: импортируемый Go SDK для любого серверного приложения.
- `internal/bundle`: основная модель managed accounts, создание, синхронизация, независимый учёт трафика.

Старые `internal/controller`, `internal/app`, `internal/store`, `internal/remnawave` обслуживают сохранённый legacy-контроллер. Его команды `check-db`, `check-remnawave`, `acceptance` относятся только к `.env.legacy.example`; для нового API они не нужны. `compose.dev.yaml` также сохранён для прежней схемы. Исторические проверки описаны в `docs/legacy-controller.md`.
