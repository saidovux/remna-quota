> Historical legacy-controller document. This does not describe the new quota-api or confirm access to a live server in the current session. See deployment.md and the root README for the new backend.

# API contracts

## Remnawave Panel

Live discovery date: 2026-09-05. Installed backend: `3.4.3` (`@remnawave/backend`). The container-provided `/opt/app/openapi.json` is 817,937 bytes with SHA-256 `ed9ca9ea55da2b9da266db8f9e1e546e9caf2831a760655a18c1ddf35218c44b`.

Authentication uses `Authorization: Bearer <API token>`. An admin login JWT is intentionally rejected for API clients unless the private browser-only header is supplied, so the controller uses its own least-privilege API token. `X-Api-Key` is reserved for an optional outer reverse proxy. In `docker_bridge` mode the client additionally sends `X-Forwarded-For: 127.0.0.1` and `X-Forwarded-Proto: https`.

Verified API-token scopes:

| Method and path | Scope | Result contract |
| --- | --- | --- |
| `GET /api/system/metadata` | `system:metadata` | `response.version` is a string |
| `GET /api/system/health` | `system:remnawave-health` | `response.runtimeMetrics[]` |
| `GET /api/users/{userId}` | `users:by-id` | numeric `response.id`, status, traffic limit, `activeInternalSquads[]` |
| `GET /api/users/by-short-uuid/{shortUuid}` | `users:by-short-uuid` | same `UserResponseDto` |
| `GET /api/users/{userId}/accessible-nodes` | `users:accessible-nodes` | `response.userId`, `activeNodes[]` |
| `GET /api/internal-squads` | `internal-squads:list` | `response.total`, `internalSquads[]` |
| `GET /api/internal-squads/{uuid}` | `internal-squads:get` | squad identity, info and inbounds |
| `GET /api/internal-squads/{uuid}/accessible-nodes` | `internal-squads:accessible-nodes` | `response.squadUuid`, `accessibleNodes[]` |
| `GET /api/bandwidth-stats/internal-squads/{squadUuid}/users/{userId}/usage` | `bandwidth-stats:internal-squad-user-usage` | `response.days[].date`, `days[].nodes[].uuid`, `days[].nodes[].totalBytes` |
| `POST /api/internal-squads/{uuid}/bulk-actions/add-many-users` | `internal-squads:add-many-users` | body `{"userIds":[number]}`, 1..1000 IDs, `202` with no body |
| `DELETE /api/internal-squads/{uuid}/bulk-actions/remove-many-users` | `internal-squads:remove-many-users` | body `{"userIds":[number]}`, 1..1000 IDs, `202` with no body |

Both membership mutations are asynchronous. A successful controller must reread `GET /api/users/{userId}` with bounded backoff and observe the desired squad membership. The mutation itself is never blindly retried after an ambiguous network result.

### Ranged usage incompatibility

The installed API requires `start` and `end` query parameters in `YYYY-MM-DD` format. Live calls with date values return zero-filled daily rows and treat both dates as included; a timestamp such as `2026-09-05T12:30:00Z` is rejected with HTTP `400` (`Validation failed`). The node byte field is confirmed as `totalBytes`.

This cannot exactly implement a quota cycle anchored at an arbitrary subscription purchase timestamp. `strict_timestamp` therefore remains the default and blocks readiness. The explicitly selected `calendar_day_utc_conservative` policy includes the complete first UTC calendar day. It may overcount, but cannot undercount. The live acceptance deployment uses this conservative policy with new users that have no traffic predating their subscriptions.

Connection dropping (`POST /api/connections/drop`) is outside MVP and remains disabled.

## Live topology snapshot

The configured MAIN and CDN accounting squads each resolve to one distinct physical node; no overlap was found on 2026-09-05. Real squad/node UUIDs and customer data are stored only in the ignored local `.env`, never in this document or fixtures.

## Bedolaga

The official current Web API source and the supplied live deployment were inspected at commit `07f3c6081233f5517200e62ad7be70aaa58ef27c` / version `4.5.0`. The live OpenAPI document was 424,673 bytes with SHA-256 `3e62ed5dddb85b197a7f2d65c35a866c575f2992efcd8c002a9120c95c79e4a6`. It uses FastAPI, supports `X-API-Key` or Bearer authentication, and exposes `/health`, `/subscriptions`, `/users/{id}`, and optionally `/openapi.json`.

`GET /subscriptions` is a bare JSON array with `limit` (1..200), `offset`, and filters. Each item includes numeric `id`, numeric `user_id`, lifecycle timestamps/status, `subscription_url`, and `connected_squads`. It also exposes `traffic_used_gb`, but the controller deliberately ignores that field for pool accounting. The response does not expose `tariff_id`, `remnawave_id`, or `remnawave_short_uuid` directly.

`GET /users/{id}` includes `subscription`/`subscriptions[]`; those summaries contain nullable `tariff_id`, so tariff lookup can be joined by subscription ID. On installations that create custom subscriptions without a tariff, the operator must explicitly configure a synthetic unassigned tariff ID; silent fallback is prohibited.

Bedolaga copies Remnawave's returned `subscriptionUrl` verbatim. The verified live shape is `https://<configured-host>/api/sub/<16-character-shortUuid>`. The controller accepts only HTTPS, the configured host, exactly this path shape, no query/fragment/userinfo, and a 16-character URL-safe Nano ID (`A-Z`, `a-z`, `0-9`, `_`, `-`); it then rereads the Remnawave user by `shortUuid` and requires an exact match. This turns the URL into a verified identity without logging or storing the URL itself.

The supplied Bedolaga Web API is enabled only on its internal Docker network. Cabinet and OpenAPI documentation remain disabled after contract discovery, and the controller uses a dedicated API token. A public API defect rejects a second active subscription even in multi-tariff mode; the acceptance-only second subscription was created through Bedolaga's own temporarily enabled internal admin API, after which the cabinet was disabled again. No Bedolaga source or database was modified.
