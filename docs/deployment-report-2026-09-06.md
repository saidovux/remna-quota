> Historical legacy-controller document. This does not describe the new quota-api or confirm access to a live server in the current session. See deployment.md and the root README for the new backend.

# Deployment and acceptance report — 2026-09-06

## Result

`remna-quota` is deployed at `/opt/remna-quota` on the supplied Bedolaga VPS. The Go controller and its dedicated PostgreSQL 18.4 container are running. PostgreSQL is isolated on a private Docker network and has no published host port. The production controller is in live write mode and reconciles every 20 seconds.

The final commercial catalog has three active 30-day products:

| Product | Price | Devices | Bedolaga representation | Independently enforced pools |
| --- | ---: | ---: | --- | --- |
| `Обычный` | 50 RUB | 3 | one subscription and one URL | MAIN 200 GiB |
| `Обход блокировок` | 100 RUB | 3 | one subscription and one URL | CDN 50 GiB |
| `Комбинированный` | 150 RUB | 3 | one subscription, one URL, and one Remnawave user | MAIN 200 GiB + CDN 50 GiB |

The combined product has a 250 GiB aggregate ceiling in Bedolaga/Remnawave, equal to the sum of its two pools. `remna-quota` applies the actual limits independently: reaching 200 GiB on MAIN removes only DIRECT access, while reaching 50 GiB on CDN removes only CDN access.

Buying `Обычный` and `Обход блокировок` separately still creates two separate subscriptions, URLs, and Remnawave users. Existing subscriptions are not merged or migrated. The combined representation is available only by buying `Комбинированный` directly, as requested.

## Bedolaga and Remnawave setup

- Bedolaga 4.5.0 remains the source of prices, subscription lifecycle, and the public catalog.
- Remnawave 3.4.3 remains the source of users, squad membership, and per-squad daily usage.
- The controller uses a dedicated Bedolaga Web API token and never connects to either application's database.
- All three tariffs are active, allow three devices, disable traffic top-ups, and use rolling 30-day reset semantics.
- Catalog order is `Обычный`, `Обход блокировок`, `Комбинированный`.
- The combined tariff and its test purchase were created through Bedolaga's own admin API while the cabinet was temporarily enabled. The cabinet was disabled again immediately afterwards; no Bedolaga source or database was modified directly.
- Bedolaga's Web API remains enabled for internal Docker-network access. Cabinet and public API documentation remain disabled.

The dedicated acceptance user now owns all three product forms. The combined purchase produced exactly one new Bedolaga subscription URL and one new Remnawave user in both DIRECT and CDN.

## Verification performed

Local verification:

- `go test ./...` — passed;
- `go test -race ./...` — passed;
- `go vet ./...` — passed;
- environment validation, Compose validation, builds, and secret/tracking scans — passed.

Live controller and contract verification:

- strict HTTPS subscription URL to Remnawave `shortUuid` mapping — passed;
- all active Bedolaga subscriptions have distinct IDs and distinct URLs;
- after the device-limit update, `check-controller` resolved all six current subscriptions to distinct Remnawave users and validated their tariff/squad mappings without writes;
- the device-limit update did not alter quota high-water values, pool states, or squad membership;
- the scheduler repeatedly completed with six subscriptions and no error or warning entries in the filtered final window;
- Bedolaga `/health`, Remnawave `/healthz`, Remnawave `/readyz`, and controller `/readyz` returned HTTP 200;
- with the cabinet disabled, `/cabinet/admin/tariffs` returned HTTP 404;
- controller and PostgreSQL containers are running, and PostgreSQL reports healthy.

## Combined-tariff acceptance

The complete production scheduler path was tested reversibly on the dedicated combined subscription by setting synthetic high-water values only in the controller's own database:

1. MAIN was set to its 200 GiB boundary. The controller recorded `main=EXHAUSTED/ABSENT`, removed DIRECT from the one Remnawave user, and left `cdn=ACTIVE/PRESENT`. The live panel showed CDN as the user's only active squad.
2. The synthetic MAIN state was removed. The next scheduler pass rebuilt zero live usage, restored DIRECT, and returned both pools to `ACTIVE/PRESENT`.
3. CDN was set to its 50 GiB boundary. The controller recorded `cdn=EXHAUSTED/ABSENT`, removed CDN, and left `main=ACTIVE/PRESENT`. The live panel showed DIRECT as the user's only active squad.
4. The synthetic CDN state was removed. The next scheduler pass restored CDN and returned both pools to `ACTIVE/PRESENT`.

The action log contains exactly one successful remove and one successful restore for each pool during this acceptance window, with no failed actions. Final combined state is MAIN and CDN both `ACTIVE/PRESENT`, with zero synthetic high-water bytes retained.

Previous reversible checks for the two standalone products also passed: exhausting the ordinary subscription did not affect its owner's separate bypass subscription, and exhausting the bypass subscription did not affect the ordinary subscription.

## Device-limit update

The device limit was increased from one to three for all three tariff definitions. Bedolaga does not retroactively copy a changed tariff limit into existing subscription rows, so all six active subscriptions were updated through Bedolaga's supported `set_device_limit` admin action. That action also synchronized each pinned subscription identity to Remnawave.

The final verification found `device_limit=3` on all six Bedolaga subscriptions and `hwidDeviceLimit=3` on all six corresponding Remnawave users. A post-restart Web API reread and a fresh read-only `check-controller` pass both succeeded.

## Security and cleanup

- The production `.env` is mode `0600`, excluded from Git, and excluded from the Docker build context.
- Temporary admin JWTs, API responses, payloads, and configuration backups used during the live check were securely removed.
- The Bedolaga cabinet configuration was byte-for-byte restored before its final restart.
- Logs were inspected with narrow time ranges, event filters, and short tails; no bulk log download was performed.
- Node SSH access was not required. Enforcement was verified through Remnawave squad membership APIs with post-mutation rereads.

## Accounting limitation

Remnawave 3.4.3 exposes inclusive UTC calendar-day usage buckets, not exact arbitrary timestamp ranges. Production therefore uses the explicitly selected `calendar_day_utc_conservative` policy. It includes the complete first UTC day and can disable access early, but it cannot undercount or allow a quota overrun.

## Operations

Safe status and filtered logs:

```bash
cd /opt/remna-quota
docker compose ps
docker compose logs --since 10m --no-color remna-quota 2>&1 \
  | grep -E 'controller_sync_complete|controller_sync_failed|quota_database_unavailable' \
  | tail -50
```
