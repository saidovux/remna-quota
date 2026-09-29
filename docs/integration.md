# Интеграция через API

remna-quota создаёт пользователей панели, управляет независимыми частями и выдаёт одну объединённую ссылку. Любой доверенный серверный клиент использует одинаковый REST API. Фронтенд обращается к вашему серверу, а сервер хранит `BACKEND_API_KEY` и проверяет права пользователя.

## Создание аккаунта и подписок

`id` выбирает вызывающее приложение: это ID **одной независимой объединённой подписки**, а не обязательно ID человека. Несколько подписок одного человека имеют разные ID и username; общий `external_ref` позволяет найти их все.

```http
PUT /api/v1/accounts/subscription-123
Authorization: Bearer <server-api-key>
Content-Type: application/json
```

```json
{
  "username": "alice",
  "name": "Основная подписка",
  "external_ref": "customer-42",
  "enabled": true,
  "parts": [
    {
      "key": "main",
      "label": "MAIN",
      "provider": "demo",
      "profile": "main",
      "enabled": true,
      "limit_bytes": 0,
      "expires_at": "2027-12-31T23:59:59Z",
      "reset_strategy": "NO_RESET"
    },
    {
      "key": "cdn",
      "label": "CDN",
      "provider": "demo",
      "profile": "cdn",
      "enabled": true,
      "limit_bytes": 75161927680,
      "expires_at": "2027-12-31T23:59:59Z",
      "reset_strategy": "NO_RESET"
    }
  ]
}
```

Для рабочей панели замените `provider` на `remnawave`, задайте нужный срок и настройте профили на сервере. В панели появятся `alice_main` и `alice_cdn`; результат содержит один `subscription_url`. 70 GiB = 75161927680 байт; если нужны десятичные 70 GB, передайте 70000000000. Ноль — безлимит. Пропуск или null у enabled/limit_bytes отклоняется.

ID: 1–80 ASCII букв, цифр, `_`, `-`. Username: 3–34 строчных букв/цифр/`_`, начинается с буквы. Ключ части: 1–10 таких символов. Полное имя `username_key` — не более 36 символов. Name/external_ref — до 128 символов без управляющих символов; external_ref не уникален и не является проверкой прав.

Username и существующие provider/profile неизменяемы. `PUT` аккаунта полностью задаёт конфигурацию: сохраняйте все существующие части, для прекращения доступа задавайте `enabled: false`. Отдельную часть можно добавить позже, поэтому конструктор может начать с одного MAIN или CDN.

Повтор идентичного PUT с тем же ID сохраняет пользователей, счётчики, ссылку и revision. `expected_revision: 0` разрешает только создание; текущая положительная revision защищает редактирование от устаревшей версии. Несовпадение возвращает `409 revision_conflict`. При потере ответа после условной записи сначала прочитайте аккаунт: повтор со старой revision закономерно получает конфликт.

## Список и состояние

```http
GET /api/v1/accounts?external_ref=customer-42&limit=50
GET /api/v1/accounts?search=alice&enabled=true&limit=50
GET /api/v1/accounts/subscription-123?refresh=true
GET /api/v1/capabilities
```

Список возвращает `{"items":[...],"next_cursor":"..."}`. Пустой next_cursor завершает обход. Следующую страницу запрашивайте с теми же фильтрами и полученным cursor. Limit — 1–100, по умолчанию 50; search — регистрозависимое начало username. Список читает сохранённые снимки без массовых запросов к панели.

Общие поля: name, external_ref, subscription_url, revision, sync_status, stale, status и active_parts. Status — `active` (все части доступны и свежие), `partial` (доступна часть), `unavailable` или `disabled`. Это последний известный результат, а не проверка сетевого подключения.

Каждая часть содержит:

- `used_bytes`, `limit_bytes`, `remaining_bytes`, `unlimited` — логический расход и последняя успешно применённая квота;
- `provider_used_bytes`, `provider_limit_bytes` — отдельные нативные показатели панели;
- `observed_total_bytes`, `counter_resets` — сохранённый наблюдаемый расход и число обнаруженных сбросов/пересозданий;
- `expires_at`, `desired_limit_bytes`, `desired_expires_at` — применённые и запрошенные параметры;
- `status`, `sync_status`, `stale`, `last_error`, `synced_at` — доступность и актуальность синхронизации;
- `observed_at`, `applied_revision`, `accounting_status` — время принятого снимка, подтверждённая revision и состояние учёта (`unknown`, `ok`, `anomaly`). У аккаунта есть `next_retry_at`, `last_attempt_at`, `failure_count`.

Unknown usage — null, не ноль. Remaining — null при безлимите или неизвестном расходе. До первого снимка limit/expiry показывают запрошенные значения. `GET ?refresh=true` только читает панель и сохраняет расход; для применения ограничений используйте sync или фоновый цикл.

## Изменение только MAIN или CDN

```http
PUT /api/v1/accounts/subscription-123/parts/cdn
Authorization: Bearer <server-api-key>
Content-Type: application/json

{
  "label": "CDN",
  "provider": "demo",
  "profile": "cdn",
  "enabled": true,
  "limit_bytes": 107374182400,
  "expires_at": "2028-01-31T23:59:59Z",
  "reset_strategy": "NO_RESET",
  "expected_revision": 1
}
```

Это полная настройка одной части; key берётся из пути, его можно также передать совпадающим полем JSON. Остальные части не перезаписываются. В примере общий лимит CDN увеличен с 70 до 100 GiB, то есть добавлено 30 GiB. Расход сохраняется. Для продления меняйте expires_at; оно само по себе не пополняет трафик.

```http
PATCH /api/v1/accounts/subscription-123
Authorization: Bearer <server-api-key>
Content-Type: application/json

{"name":"Телефон","enabled":false,"expected_revision":2}
```

PATCH изменяет name, external_ref и/или enabled. Отключение всего аккаунта отключает его пользователей в панели, сохраняя индивидуальные флаги частей для последующего включения. Само включение не разблокирует исчерпанную или истёкшую часть.

## Ошибки, синхронизация и общая ссылка

Состояние сначала сохраняется в PostgreSQL, затем применяется по частям вне SQL-транзакций. Запрос ждёт до пяти секунд: `200` с sync_status=ready означает успешную синхронизацию; `202` означает сохранённые настройки с незавершённой/неудачной синхронизацией. Отмена HTTP-запроса не отменяет обработку. Проверяйте per-part last_error/stale и applied_revision; `GET` без refresh и список не ждут панель. Фоновый цикл продолжит обработку без дублей с сохранённой задержкой повторов; ручной `POST /api/v1/accounts/{id}/sync` также учитывает `Retry-After`. Нет транзакции сразу между PostgreSQL и Remnawave.

Для мониторинга используйте `GET /api/v1/diagnostics` или Prometheus-маршрут `GET /api/v1/metrics` с тем же Bearer-ключом. Go SDK: `api.Diagnostics(ctx)`. Метрики не содержат ID пользователей, ссылок и токенов.

Ошибки имеют вид `{"error":"machine_code"}`. Основные коды: 401 unauthorized, 404 not_found, 409 account_conflict/revision_conflict, 422 invalid_account, 503 provider_unavailable. Идентичное имя чужого пользователя панели не присваивается.

Общий URL доступен без API-ключа: храните его как секрет. Форматы — Base64 по умолчанию или plain. Публичный маршрут читает текущие состояния и исключает исчерпанные/истёкшие части, оставляя остальные. Он не сбрасывает квоты.

`POST /api/v1/accounts/{id}/rotate-token` заменяет URL; уже импортированные VPN-ключи не меняются. Для прекращения VPN-доступа отключайте аккаунт/часть. `POST /api/v1/subscriptions/resolve` с JSON `{"subscription_url":"..."}` разрешает локальный URL в аккаунт, требует серверного ключа и не создаёт пользовательскую сессию.

## Go SDK

```go
api, err := client.New("https://quota.example", os.Getenv("QUOTA_API_KEY"))
if err != nil { return err }

page, err := api.ListAccounts(ctx, client.ListAccountsOptions{
    ExternalRef: "customer-42", Limit: 50,
})
if err != nil { return err }
_ = page

account, err := api.GetAccount(ctx, "subscription-123", true)
if err != nil { return err }

cdn := client.PartRequest{
    Key: "cdn", Label: "CDN", Provider: "remnawave", Profile: "cdn",
    Enabled: true, LimitBytes: 100 << 30, ExpiresAt: expiry,
    ResetStrategy: "NO_RESET",
}
account, err = api.PutPart(ctx, account.ID, cdn, &account.Revision)
if err != nil { return err }
// Только ready подтверждает применение всех настроек.
if account.SyncStatus != "ready" { /* показать ожидание и повторить sync позже */ }
```

Импорт: `github.com/saidovux/remna-quota/pkg/client`. Также доступны PutAccount, PatchAccount, SyncAccount, RotateToken, ResolveSubscription, Capabilities. Полный контракт — [OpenAPI](openapi.yaml).

Для объединения существующих чужих подписок без их изменения есть отдельный [API bundles](integration-aggregation.md). Он не заменяет описанный здесь управляемый жизненный цикл.

Общий лимит устройств для MAIN и CDN задаётся через `device_limit` аккаунта. Список и удаление устройств доступны через API. См. [HWID](hwid.md).
