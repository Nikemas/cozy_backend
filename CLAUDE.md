# cozy_backend

- Миграции: новая — `make migrate-new name=<имя>` (golang-migrate, нумерация `-seq`). Уже применённые миграции не редактировать: staging и prod их уже прогнали, нужна новая миграция. У каждой `up` должна быть рабочая `down`. Откат деплоя миграции НЕ откатывает, поэтому миграция должна работать и со старым кодом (expand/contract: сначала добавить, удалять релизом позже).
- Меняешь API — сначала обнови `openapi.yaml`: по нему работает мобилка (`cozy_mobile`).
- Тексты интерфейса и ошибок — только через `locales/` (`ru.yaml`/`ky.yaml`, `admin.*`, `errors.*`), оба языка сразу. Сайт выбирает язык из `?lang=ru|ky`, потом из cookie `cozy_lang`.
- Интеграционные тесты: `make test-integration` (тег `integration`, нужна локальная БД из `make dev-up`).
- `scripts/deploy.sh` запускается на VPS из GitHub Actions, вручную его не вызывать — см. скилл `cozy-deploy`.
