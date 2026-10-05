# Ранбук демо сайта (staging, 14.10.2026)

Основной документ — чек-лист `tasks/demo-launch-checklist.md` (в корне проекта Cozy, рядом с `cozy_backend/`): заморозка main, бэкап, проверка `.env`, чистка тестовых данных, откат образа и БД, мониторинг, риски. Здесь только то, чего там нет: smoke-тест, проверка версии миграций и диагностика.

Команды с пометкой «VPS» выполняет пользователь по SSH из `/opt/cozy`:

```bash
cd /opt/cozy
C="docker compose -f docker/docker-compose.prod.yml --env-file .env"
```

## 1. Smoke-тест (`scripts/smoke.sh`)

Только GET-запросы, ничего не пишет; запускается с ноутбука (или на VPS).

```bash
bash scripts/smoke.sh https://cozy.erpsystemsales.com              # категория/товар берутся с главной
bash scripts/smoke.sh https://cozy.erpsystemsales.com muzhskoe     # конкретная категория
bash scripts/smoke.sh https://cozy.erpsystemsales.com muzhskoe <slug-товара>
```

Что проверяется: `/healthz`, `/readyz` (db+minio ok), главная, категория, карточка товара (JSON-LD, og:image), `/cart` гостем (303 на `/profile` — так задумано), `/profile`, `/about /contacts /delivery /privacy /terms` на RU и KY (язык через `?lang=ru|ky`, проверяется `<html lang="..">`), `/branches`, `/sitemap.xml`, `/robots.txt`, `/api/v1/points`, `/api/v1/app/config`, HTML-404 и JSON-404 под `/api/`. На каждой HTML-странице — нет `{{` (утечка шаблона) и нет `[ЗАПОЛНИТЬ`.

Результат: строка `PASS`/`FAIL` на каждую проверку, в конце список провалов. Код выхода = число FAIL (0 — всё зелёное). Таймаут запроса — `SMOKE_TIMEOUT` (по умолчанию 15 с).

Когда запускать: после финального деплоя 13.10, после чистки данных, после любого отката, утром 14.10. Пока реквизиты не вписаны, 10 FAIL `[ЗАПОЛНИТЬ` на статических страницах (5 × RU/KY) ожидаемы; всё остальное должно быть PASS.

## 2. Версия миграций (VPS)

```bash
bash scripts/migrate.sh version                         # напр. "38"; "(dirty)" = упавшая миграция
ls migrations/*.up.sql | tail -n1                        # последняя миграция в задеплоенном коде
git rev-parse --short=12 HEAD                            # задеплоенный SHA
docker inspect -f '{{.Config.Image}}' "$($C ps -q backend)"   # тег образа, который реально работает
```

Номер из `version` должен совпадать с номером последнего файла. `dirty` или расхождение — не деплоить и не откатывать, разбираться по README «Миграции: dirty и восстановление». Образ, на который откатываетесь, может быть старше схемы: это безопасно только при expand/contract (чек-лист п. 2.2).

## 3. Красный `/readyz`

```bash
curl -sS -i https://cozy.erpsystemsales.com/readyz       # 503 + какая проверка упала: db / minio
curl -sS -o /dev/null -w '%{http_code}\n' https://cozy.erpsystemsales.com/healthz
```

| Симптом | Что смотреть (VPS) | Что делать |
|---|---|---|
| `502`/таймаут на всё, `/healthz` тоже не 200 | `$C ps backend`, `$C logs --tail=100 backend` | `$C up -d backend`; падает при старте — проблема в `.env` (backend не стартует при невалидном конфиге, текст ошибки в логе) или в образе → откат (чек-лист п. 2.2) |
| `"db":"fail"` | `$C ps postgres`, `$C exec -T postgres pg_isready -U cozy -d cozy`, `$C logs --tail=100 postgres`, `df -h /` | `$C up -d postgres`; переполнен диск — освободить (`docker image prune -f`, старые дампы), не трогая свежий дамп |
| `"minio":"fail"` | `$C ps minio`, `$C logs --tail=100 minio`, `df -h /` | `$C restart minio`; пропали фото — восстановление медиа (чек-лист п. 2.4) |
| `/readyz` 200, но сайт с ошибками | `X-Request-ID` из ответа → `$C logs backend \| grep <id>` | точечно; если ломается оформление заказа — п. 2.6 чек-листа |
| TLS/сертификат | `$C logs --tail=100 caddy` | чек-лист п. 2.5 |

После любого действия — `bash scripts/smoke.sh https://cozy.erpsystemsales.com`.

## 4. Откат: дополнения к чек-листу

Команды отката — чек-лист п. 2.2 (образ) и 2.3 (БД), они совпадают с `rollback_and_fail` в `scripts/deploy.sh` и README. Дополнительно:

- Остановить автодеплой на время демо с ноутбука (нужен `gh` с правами на репозиторий): `gh workflow disable Deploy`; после демо — `gh workflow enable Deploy`. Не откатывайтесь перезапуском старого workflow: у старого коммита нет новых миграций, `migrate up` упадёт.
- Тег `demo-ready` (чек-лист п. 2.2) `deploy.sh` при чистке считает обычным `<sha>`-тегом: после 5 новых деплоев он может быть удалён. При заморозке main это не страшно; перед откатом проверьте `docker image ls cozy-backend`.
- Проверка, что откат применился: `docker inspect -f '{{.Config.Image}}' "$($C ps -q backend)"` → `cozy-backend:previous` (или `demo-ready`), затем smoke.

## 5. Мониторинг во время демо и сброс данных

Мониторинг — чек-лист п. 3.1 (цикл `/readyz`, логи backend, request-id). Smoke во время показа не гонять в цикле (нагрузки он не даёт, но засоряет логи); при подозрении — один прогон.

Сброс тестовых данных — чек-лист п. 1.4 (через админку, после бэкапа п. 1.1). Полный возврат к состоянию до демо — восстановление дампа (п. 2.3), затем smoke.
