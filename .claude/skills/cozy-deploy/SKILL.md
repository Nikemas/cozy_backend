---
name: cozy-deploy
description: Деплой cozy_backend на staging (cozy.erpsystemsales.com) или prod (cozy.kg) через GitHub Actions, проверка результата и откат. Использовать при «задеплой», «выкати», «залей на staging/прод», «почему не обновился сервер».
---

# Деплой Cozy

Деплой идёт только через GitHub Actions. На сервере `scripts/deploy.sh` делает hard reset на нужный коммит, поэтому правки кода прямо на сервере сотрутся. ssh на VPS нужен только для диагностики.

## Staging
1. Закоммить и запушь в `main` репозитория cozy_backend. Workflow `deploy.yml` запустится сам: сначала CI, потом деплой по SSH.
2. Следи за запуском: `gh run list --workflow deploy.yml -L 3`, затем `gh run watch <id> --exit-status`.
3. Перезапуск без нового коммита (например, после смены секрета): `gh workflow run deploy.yml`.
4. Проверка: `bash scripts/smoke.sh https://cozy.erpsystemsales.com`. Код выхода 0 значит, что все проверки прошли.

## Prod (только после явного «да» пользователя в чате)
1. Узнай точный ref: тег или SHA, который уже есть в `main` и прошёл staging.
2. Назови пользователю ref и что войдёт в релиз, дождись подтверждения.
3. Запусти: `gh workflow run deploy-prod.yml -f ref=<tag|sha>` и следи через `gh run watch`.
4. Проверка: `bash scripts/smoke.sh https://cozy.kg`.

## Миграции и откат
- deploy.sh применяет миграции до переключения образа. Если `/readyz` не поднялся, скрипт сам возвращает образ `:previous`.
- Откат НЕ откатывает миграции. Поэтому миграция должна быть совместима с кодом предыдущего релиза (expand/contract): сначала добавить колонку или таблицу, удалять их только релизом позже.
- Ручной откат: задеплоить предыдущий рабочий SHA тем же workflow.

## Если деплой упал
- Смотри лог шага: `gh run view <id> --log-failed`.
- Ошибка «dirty or untracked migration state»: сначала выясни причину, `make migrate-force` вслепую не запускать.
