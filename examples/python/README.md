# Вызов из бота или Python-бэкенда

Используется стандартная библиотека Python. API-ключ остаётся на сервере; никаких изменений в remna-quota для Telegram не требуется. Клиент не следует редиректам и не включает секреты в ошибки.

```python
import os
from quota_client import QuotaClient, QuotaError

api = QuotaClient(os.environ["QUOTA_API_URL"], os.environ["QUOTA_API_KEY"])

# ID отдельного набора из вашей базы. Несколько наборов могут иметь одного владельца.
bundle_id = "subscription-123"
bundle = api.request("PUT", f"/api/v1/bundles/{bundle_id}", {
    "name": "MAIN + CDN",
    "external_ref": "telegram-user-42",
    "enabled": True,
    "sources": [
        {"key": "main", "label": "MAIN", "provider": "remnawave", "reference": "101", "enabled": True},
        {"key": "cdn", "label": "CDN", "provider": "remnawave", "reference": "102", "enabled": True},
    ],
})
# Передайте bundle["subscription_url"] только получателю, которому разрешён доступ.

observed = api.request("GET", f"/api/v1/bundles/{bundle_id}", query={"refresh": "true"})

# Атомарно отсоединить CDN; исходный пользователь панели остаётся без изменений.
try:
    api.request("DELETE", f"/api/v1/bundles/{bundle_id}/sources/cdn",
                query={"expected_revision": observed["revision"]})
except QuotaError as error:
    if error.status == 409:
        # Перечитать состояние и пересчитать изменение, не затирать чужое обновление.
        pass
    else:
        raise
```

В demo замените провайдера на `demo`, ID источников на `main`/`cdn`. Для настоящей панели укажите ID подписок, проверенные вашим приложением. Не используйте произвольные данные сообщения Telegram как готовый путь API; ID разрешает только латинские буквы, цифры, `_` и `-`.
