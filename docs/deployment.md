# Cozy — руководство по развёртыванию и эксплуатации backend

Документ для администратора сервера / разработчика сопровождения. Описывает, как
поднять Cozy (сайт + админ-панель + REST API для мобильного приложения) на
чистом VPS, как деплоить, откатывать, делать и восстанавливать бэкапы.

Всё, что ниже, сверено с файлами репозитория `cozy_backend` на 06.10.2026:
`internal/config/*.go`, `internal/orders/settings.go`, `cmd/server/notifications.go`,
`.env.example`, `docker/*`, `scripts/*.sh`, `.github/workflows/*.yml`. Если код
поменялся — источник истины код и `.env.example`, а не этот текст.

Связанные документы: [`../README.md`](../README.md) (разработка, CI, подробности
по миграциям), [`runbook-demo.md`](runbook-demo.md) (smoke-тест и диагностика),
[`admin-guide.md`](admin-guide.md) (работа в админке), `openapi.yaml` (API).

---

## 1. Архитектура в двух словах

Один Go-бинарник (`cmd/server`) отдаёт три продукта на одном домене:

| Путь | Что |
|---|---|
| `/` | сайт-магазин (HTML, `web/templates`) |
| `/admin` | админ-панель (HTML, `admin/templates`) |
| `/api/v1/*` | REST API мобильного приложения (`openapi.yaml`) |
| `/healthz`, `/readyz` | проверки живости / готовности |

На сервере всё работает в Docker Compose (`docker/docker-compose.prod.yml`):

| Сервис | Образ | Назначение |
|---|---|---|
| `backend` | `cozy-backend:<sha>` — собирается на самом сервере из `docker/Dockerfile` | Go-сервер, порт 8080 только внутри сети compose |
| `postgres` | `postgres:16-alpine` | база `cozy`, роль `cozy`, том `postgres_data` |
| `minio` | `quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z` | фото товаров и баннера, бакет `MINIO_BUCKET`, том `minio_data` |
| `caddy` | `caddy:2.11.4-alpine` | HTTPS (Let's Encrypt автоматически), порты 80/443, reverse proxy на `backend` и `minio` |

Container registry не используется: образ собирается на VPS из git-checkout
`/opt/cozy`. Наружу открыты только 80/443 (Caddy) и SSH. Postgres и MinIO
наружу не публикуются.

Внешние сервисы, от которых зависит работа:

| Сервис | Для чего | Переменные |
|---|---|---|
| Nikita SMSPro (`smspro.nikita.kg`, «СЕРВИС OTP») | SMS-код для входа покупателей | `NIKITA_API_KEY` |
| Bakai OpenBanking | онлайн-оплата (ссылка на оплату, опционально QR) | `BAKAI_*`, `PAYMENTS_PROVIDER` |
| Firebase Cloud Messaging (проект `cozy-7f364`) | push покупателям: статусы заказов и рассылки | `FCM_CREDENTIALS_FILE`, `FCM_PROJECT_ID` |
| Telegram Bot API | уведомление персоналу о новом заказе | `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` |
| Let's Encrypt | TLS-сертификаты (Caddy получает и продлевает сам) | — |
| Off-site хранилище (Backblaze B2 / любой S3 через rclone) — опционально | копия бэкапов вне VPS | `BACKUP_RCLONE_REMOTE` (переменная cron, не `.env`) |
| healthchecks.io — опционально | алерт, если ночной бэкап не прошёл | `BACKUP_HEALTHCHECK_URL` (переменная cron) |

---

## 2. Окружения: staging и prod

| | staging (работает сейчас) | prod (к сдаче) |
|---|---|---|
| Домен | `cozy.erpsystemsales.com`, `media.cozy.erpsystemsales.com` | `cozy.kg`, `www.cozy.kg`, `media.cozy.kg` |
| VPS | `95.215.244.199`, каталог `/opt/cozy` | **отдельный VPS** (рекомендуется), каталог `/opt/cozy` |
| `APP_ENV` | `staging` | `prod` |
| Оплата | `PAYMENTS_PROVIDER=mock` допустим | только `bakai` (`mock` — отказ старта) |
| SMS | `SMS_MOCK_OTP=true` допустим (код всегда `0000`) | `SMS_MOCK_OTP=true` — отказ старта |
| Поисковики | `X-Robots-Tag: noindex` на всех хостах | без `noindex` |
| Деплой | автоматически на каждый push в `main` (`.github/workflows/deploy.yml`) | только вручную: `.github/workflows/deploy-prod.yml` (тег/SHA, подтверждение в Environment `production`) |
| Стек на сервере | compose-проект `docker` (имя по умолчанию), `docker/Caddyfile`, образ `cozy-backend` | compose-проект `cozy-prod`, `docker/Caddyfile.prod` (хосты из `SITE_HOST`/`MEDIA_HOST`), образ `cozy-backend-prod` |
| Бэкапы | `scripts/backup.sh` по cron | то же, свой off-site remote и свой чек healthchecks |

Почему prod нельзя «просто добавить» вторым доменом в staging-`Caddyfile`:
покупатели работали бы со staging-базой и mock-оплатой. Prod — отдельный
стек со своей базой, своим MinIO, своим `.env` и своими секретами.

Какой стек обслуживают скрипты, задаёт переменная `DEPLOY_ENV`
(`scripts/env.sh`, общий для `deploy.sh`, `migrate.sh`, `backup.sh`):

| | `DEPLOY_ENV=staging` (по умолчанию) | `DEPLOY_ENV=production` |
|---|---|---|
| compose-проект | не задаётся (`docker`, как и раньше — тома staging не меняются) | `cozy-prod` (или `COMPOSE_PROJECT_NAME`) |
| env-файл | `.env` (или `ENV_FILE`) | `.env` (или `ENV_FILE`) |
| Caddyfile | `docker/Caddyfile` | `docker/Caddyfile.prod` |
| образ backend | `cozy-backend:<sha>` | `cozy-backend-prod:<sha>` |
| `DEPLOY_REF` | по умолчанию `origin/main` | **обязателен** (тег или SHA) |
| `HEALTH_URL` | `https://cozy.erpsystemsales.com/readyz` | `https://$SITE_HOST/readyz` |
| доп. проверки | — | в env-файле заданы `SITE_HOST`, `MEDIA_HOST`, `APP_ENV=prod` |

Prod всё равно ставится на **отдельный VPS**: оба стека слушают 80/443, и
два Caddy на одном сервере не уживутся. Имена проекта и образов разведены,
чтобы ошибочная команда на чужом сервере не задела чужие тома.

---

## 3. Требования к серверу

- Ubuntu 22.04/24.04 LTS (или любой Linux с Docker Engine), x86_64.
- Рекомендация по ресурсам: 2 vCPU, 4 ГБ RAM, 40+ ГБ SSD (база небольшая,
  место занимают фото в MinIO, локальные бэкапы — дампы за 14 дней и зеркало
  медиа — и Docker-образы: хранится 5 последних сборок).
- Установлено: `git`, `curl`, Docker Engine + плагин `docker compose`,
  `util-linux` (`flock`), опционально `rclone` (иначе `backup.sh` запускает
  образ `rclone/rclone:1.75.1`).
- Открыты входящие порты 22 (SSH), 80 и 443. Порт 80 нужен Caddy для выпуска
  сертификата Let's Encrypt (HTTP-01) и редиректа на HTTPS.
- DNS-записи домена указывают на IP сервера (раздел 5.3).

---

## 4. Переменные окружения

Все настройки — переменные окружения из файла `/opt/cozy/.env` (рядом с
репозиторием, в git не попадает; шаблон — `.env.example`). Compose передаёт
его в `backend` целиком (`env_file: ../.env`), а `caddy` читает из него
`BAKAI_WEBHOOK_TOKEN`. В `docker-compose.prod.yml` переопределены
`DATABASE_URL` (собирается из `POSTGRES_PASSWORD`) и `MINIO_ENDPOINT=minio:9000`
— их в `.env` для сервера задавать не нужно.

Сервер проверяет конфигурацию при старте и **не запускается** при ошибке —
текст ошибки с именем переменной будет в `docker compose ... logs backend`.

Обозначения в колонке «prod»: **обяз.** — без неё сервер не стартует при
`APP_ENV=prod`; **нужно** — сервер стартует, но функция не работает;
«—» — необязательно.

### 4.1. Основные

| Переменная | По умолчанию | prod | Описание |
|---|---|---|---|
| `APP_ENV` | — (пусто = ошибка) | **обяз.** `prod` | `dev` / `staging` / `prod`. `staging` и `prod` требуют реальных секретов (см. ниже); `prod` дополнительно запрещает mock-оплату и mock-SMS |
| `HTTP_ADDR` | `:8080` | — | адрес, который слушает backend внутри контейнера. Менять не нужно: Caddy проксирует на `backend:8080` |
| `DATABASE_URL` | — | задаётся compose | строка подключения к Postgres. В prod-compose подставляется автоматически |
| `POSTGRES_PASSWORD` | — | **обяз.** | пароль роли `cozy` в контейнере Postgres (используется compose). Генерировать `openssl rand -hex 32`: hex, потому что пароль подставляется в URL и символы `@ : / ?` его ломают |
| `JWT_SECRET` | — | **обяз.** | ключ подписи токенов мобильного приложения, не короче 32 байт, не `change-me…`. `openssl rand -hex 32`. Свой для каждого окружения. Смена = все покупатели в приложении будут разлогинены |
| `PUBLIC_BASE_URL` | — | **обяз.** `https://cozy.kg` | внешний адрес сайта без `/` в конце, обязательно `https://`. Используется для ссылок оплаты, адреса возврата из банка и ссылок на заказ в Telegram |
| `LOG_FORMAT` | `text` | — (рекомендуется `json`) | `text` или `json` (одна JSON-строка на запись) |

### 4.2. MinIO (фото)

| Переменная | По умолчанию | prod | Описание |
|---|---|---|---|
| `MINIO_ENDPOINT` | `localhost:9000` | задаётся compose (`minio:9000`) | адрес MinIO для самого backend |
| `MINIO_ACCESS_KEY` | — | **обяз.** | логин MinIO (он же `MINIO_ROOT_USER` контейнера). `openssl rand -hex 20` |
| `MINIO_SECRET_KEY` | — | **обяз.** | пароль MinIO (он же `MINIO_ROOT_PASSWORD`). `openssl rand -hex 20`. Не должен содержать `/`, `@`, `:` (используется в URL в `backup.sh`) |
| `MINIO_BUCKET` | `cozy-media` | — | бакет. Создаётся при старте автоматически и открывается на анонимное чтение |
| `MINIO_USE_SSL` | `false` | — | TLS до MinIO изнутри сети compose — не нужен |
| `MINIO_PUBLIC_ENDPOINT` | = `MINIO_ENDPOINT` | **нужно** `media.cozy.kg` | хост, с которого браузер и приложение грузят фото. Без него ссылки на фото указывают на `minio:9000`, и ни одно фото не загрузится |
| `MINIO_PUBLIC_USE_SSL` | = `MINIO_USE_SSL` | **нужно** `true` | фото по `https://` |

### 4.3. Оплата (Bakai)

| Переменная | По умолчанию | prod | Описание |
|---|---|---|---|
| `PAYMENTS_PROVIDER` | `bakai` при `APP_ENV=prod`, иначе `mock` | `bakai` | `mock` — тестовая страница вместо банка (кто угодно «оплатит» заказ бесплатно) — запрещено в prod |
| `BAKAI_API_TOKEN` | — | **обяз.** при `bakai` | bearer-токен Bakai OpenBanking для создания ссылки на оплату. Выдаётся банком через одноразовые учётные данные мерчанта (`POST /Auth/Login`), срока и обновления нет — при ответах 401 перевыпускать вручную |
| `BAKAI_WEBHOOK_TOKEN` | — | **обяз.** при `bakai` | общий секрет вебхука. Генерировать `openssl rand -hex 32`. Caddy сам добавляет его в заголовок `X-Webhook-Token` на маршруте `/api/v1/payments/bakai/webhook`; в кабинете банка регистрируется **голый URL без query**: `https://cozy.kg/api/v1/payments/bakai/webhook` |
| `BAKAI_QR_TOKEN`, `BAKAI_ACCOUNT_NO` | — | — | для кнопки «Показать QR» на странице оплаты сайта (сервис Bakai «Создание QR кода для оплаты»). Пусто — кнопки нет, оплата по ссылке работает |
| `BAKAI_CURRENCY_ID` | `417` (KGS) | — | код валюты для QR |
| `BAKAI_BASE_URL` | `https://openbanking-api.bakai.kg` | — | адрес API банка |
| `PAYMENT_PENDING_TTL` | `30m` | — | через сколько неоплаченный онлайн-заказ отменяется, а товар возвращается в остаток. Минимум `1m` |

### 4.4. SMS (Nikita)

| Переменная | По умолчанию | prod | Описание |
|---|---|---|---|
| `NIKITA_API_KEY` | — | **обяз.** | API-ключ OTP: smspro.nikita.kg → Личный кабинет → вкладка «СЕРВИС OTP». Без него покупатели не смогут войти |
| `SMS_MOCK_OTP` | `false` | **обяз.** `false` | `true` — SMS не отправляются, код всегда `0000`. Только для dev/staging; с `APP_ENV=prod` сервер не стартует |
| `REVIEW_PHONE` | — | опц. | Номер аккаунта для ревью App Store / Google Play (любой формат КР, нормализуется в `+996XXXXXXXXX`). Для него SMS не отправляется, вход по `REVIEW_OTP_CODE`. Только вместе с `REVIEW_OTP_CODE`; иначе отказ старта |
| `REVIEW_OTP_CODE` | — | опц. | Фиксированный код ревью-аккаунта: ровно 4 цифры, не `0000`/`1234`/повторы/последовательности (отказ старта). Хранить как секрет, в логи не попадает. Срок жизни, лимит попыток и частоты запросов — как у обычного OTP. Не используется для других номеров. После 10 неверных ревью-кодов (всего с запуска процесса) вход по коду отключается до перезапуска: номер получает обычную SMS, в лог пишется ERROR и в Telegram (если настроен) уходит алерт — сменить код и перезапустить. После ревью — очистить обе переменные |
| `SMS_STATUS_FALLBACK` | `false` | — | SMS о смене статуса заказа покупателю без push. **Пока не реализовано**: у Nikita подключён только OTP API, при `true` сообщения только пишутся в лог |

### 4.5. Уведомления

| Переменная | По умолчанию | prod | Описание |
|---|---|---|---|
| `FCM_CREDENTIALS_FILE` | — | **нужно** `/secrets/firebase-service-account.json` | JSON-ключ service account Firebase (Firebase Console → Project settings → Service accounts → Generate new private key). Файл кладётся в `/opt/cozy/secrets/` (монтируется в контейнер как `/secrets`, read-only), права `chmod 644` (контейнер работает не от root). Пусто или файл битый — сервер стартует, push только пишутся в лог (`push: FCM misconfigured`) |
| `FCM_PROJECT_ID` | из JSON-ключа | — | переопределить project_id |
| `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` | — | **нужно** | бот от @BotFather, добавленный в группу персонала; `TELEGRAM_CHAT_ID` — id группы (отрицательное число, например `-1001234567890`). Любая пустая — уведомления о новых заказах только в лог |

### 4.6. Заказы и доставка

| Переменная | По умолчанию | Описание |
|---|---|---|
| `DELIVERY_FEE_SOM` | `200` | стоимость доставки, сом, если в админке нет ни одной активной зоны доставки (иначе цену берут из зон, см. [admin-guide.md](admin-guide.md)) |
| `MAX_OPEN_ORDERS_PER_CUSTOMER` | `5` | сколько незавершённых заказов может быть у покупателя; `0` — без лимита |

### 4.7. Мобильное приложение и контакты магазина

| Переменная | По умолчанию | prod | Описание |
|---|---|---|---|
| `APP_MIN_VERSION` | `1.0.0` | — | ниже этой версии приложение требует обновиться (`GET /api/v1/app/config`) |
| `APP_LATEST_VERSION` | `1.0.0` | — | последняя версия приложения |
| `APP_STORE_URL_IOS`, `APP_STORE_URL_ANDROID` | — | **нужно** после публикации | ссылки на сторы (экран «Обновите приложение», страница `/about`) |
| `SHOP_PHONE`, `SHOP_WHATSAPP` | — | **нужно** | телефон и WhatsApp как показывать, например `+996 555 123 456` |
| `SHOP_TELEGRAM` | — | — | `@username` |
| `SHOP_EMAIL` | — | **нужно** | e-mail поддержки |
| `SHOP_HOURS` | — | — | режим работы, свободный текст без слов (показывается на RU и KY), например `10:00–20:00` |
| `SHOP_BANK_DETAILS` | — | **нужно** | банк, р/с, БИК одной строкой (оферта) |
| `SHOP_LEGAL_NAME`, `SHOP_INN`, `SHOP_LEGAL_ADDRESS` | — | **нужно** | продавец / оператор персональных данных (футер, `/contacts`, `/privacy`, `/terms`, `/account-deletion`). ИНН — 10–14 цифр |

Пустая `SHOP_*` — строка на странице не выводится; значение неверного
формата — сервер не стартует.

### 4.8. Тонкая настройка (менять обычно не нужно)

| Переменная | По умолчанию | Описание |
|---|---|---|
| `DB_MAX_OPEN_CONNS` / `DB_MAX_IDLE_CONNS` | `25` / `10` | пул соединений к Postgres |
| `DB_CONN_MAX_LIFETIME` / `DB_CONN_MAX_IDLE_TIME` | `30m` / `5m` | время жизни соединений |
| `HTTP_READ_HEADER_TIMEOUT` | `5s` | защита от медленных клиентов |
| `HTTP_READ_TIMEOUT` / `HTTP_WRITE_TIMEOUT` | `60s` / `60s` | чтение запроса (включая загрузку xlsx) / запись ответа |
| `HTTP_IDLE_TIMEOUT` | `120s` | keep-alive |
| `HTTP_SHUTDOWN_TIMEOUT` | `10s` | плавная остановка при деплое |
| `COOKIE_SECURE` | `true` (кроме `dev`) | флаг Secure у cookie |
| `TRUSTED_PROXY_CIDRS` | loopback + частные сети | от кого принимать `X-Forwarded-For` (Caddy в сети Docker). Пусто — заголовок игнорируется |
| `RATE_LIMIT_RPS` | `20` | общий лимит запросов в секунду с одного IP клиента (token bucket; IPv6 — по /64; дробное можно; `0` — выключить). Не считаются `/healthz`, `/readyz`, `/static/*`, `/admin/static/*` и вебхук оплаты. Превышение — `429` с `Retry-After` (JSON-ошибка `rate_limited` для `/api/*`, простая страница для сайта) |
| `RATE_LIMIT_BURST` | `60` | сколько запросов с одного IP можно сделать разом, прежде чем включится `RATE_LIMIT_RPS` (минимум 1). Запас на экран приложения с несколькими параллельными запросами и на абонентов за одним IP мобильного оператора |
| `MAX_BODY_BYTES` | `1048576` (1 МиБ) | лимит тела JSON/форм |
| `MAX_UPLOAD_BYTES` | `26214400` (25 МиБ) | лимит multipart-загрузок (Caddy дополнительно режет всё больше 20 МБ) |
| `OTP_MAX_PER_IP_PER_HOUR` | `30` | запросов SMS-кода с одного IP в час (`0` — отключить) |
| `OTP_MAX_PER_DAY` | `1000` | всего SMS-кодов за 24 ч на всех — потолок расходов на SMS |
| `OTP_VERIFY_MAX_ATTEMPTS` | `5` | попыток ввода одного кода (минимум 1) |
| `OTP_VERIFY_MAX_FAILS_PER_IP_PER_HOUR` | `30` | неверных кодов с одного IP в час |
| `AUTH_REFRESH_MAX_PER_IP_PER_MINUTE` | `120` | обновлений токена с одного IP в минуту |
| `STAFF_LOGIN_MAX_PER_IP` | `20` | попыток входа в админку с одного IP за 15 минут (плюс 5 на один телефон — в коде) |
| `OTP_RETENTION_DAYS` | `30` | сколько дней хранить записи о запросах кода (телефон, IP); минимум 2. Выводится на `/privacy` |

### 4.9. Чек-лист `.env` для prod

- [ ] `APP_ENV=prod`, `LOG_FORMAT=json`
- [ ] `POSTGRES_PASSWORD`, `JWT_SECRET`, `MINIO_ACCESS_KEY`, `MINIO_SECRET_KEY`, `BAKAI_WEBHOOK_TOKEN` — новые случайные значения, **не** такие же, как на staging
- [ ] `PUBLIC_BASE_URL=https://cozy.kg`
- [ ] `SITE_HOST=cozy.kg`, `MEDIA_HOST=media.cozy.kg` — хосты для `docker/Caddyfile.prod` (`www.SITE_HOST` редиректит на `SITE_HOST`); без них `deploy.sh` не стартует
- [ ] `MINIO_PUBLIC_ENDPOINT=media.cozy.kg`, `MINIO_PUBLIC_USE_SSL=true`
- [ ] `PAYMENTS_PROVIDER=bakai`, `BAKAI_API_TOKEN` — боевой токен **мерчант-аккаунта заказчика** (не общий с другими проектами)
- [ ] `NIKITA_API_KEY` — ключ кабинета Nikita заказчика, `SMS_MOCK_OTP=false`
- [ ] `FCM_CREDENTIALS_FILE=/secrets/firebase-service-account.json`, файл на месте, `chmod 644`
- [ ] `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` — бот и группа персонала prod (не тестовая)
- [ ] `APP_MIN_VERSION`, `APP_LATEST_VERSION`, `APP_STORE_URL_IOS`, `APP_STORE_URL_ANDROID`
- [ ] `SHOP_*` — контакты и реквизиты заказчика
- [ ] `chmod 600 /opt/cozy/.env`, владелец — пользователь `deploy`

---

## 5. Развёртывание с нуля

Команды выполняются на prod-VPS. Чтобы все команды ниже (и ручные
`scripts/*.sh`) работали с prod-стеком, один раз добавьте в
`~deploy/.profile` (и выполните в текущей сессии):

```bash
export DEPLOY_ENV=production COMPOSE_PROJECT_NAME=cozy-prod \
  CADDYFILE=Caddyfile.prod BACKEND_IMAGE=cozy-backend-prod
```

Для краткости:

```bash
C="docker compose -f docker/docker-compose.prod.yml --env-file .env"
```

На staging-VPS эти переменные **не** задаются — там всё как раньше.

### 5.1. Подготовка сервера (под root)

```bash
apt update && apt upgrade -y
apt install -y git curl ca-certificates ufw
# Docker Engine + compose plugin — официальный скрипт или по инструкции docs.docker.com/engine/install/ubuntu
curl -fsSL https://get.docker.com | sh
docker compose version

ufw allow 22/tcp && ufw allow 80/tcp && ufw allow 443/tcp && ufw enable

# пользователь для деплоя (docker без sudo)
adduser --disabled-password --gecos "" deploy
usermod -aG docker deploy
install -d -m 700 -o deploy -g deploy /home/deploy/.ssh
# в /home/deploy/.ssh/authorized_keys — публичные ключи администраторов и ключ GitHub Actions
```

Вход на сервер — только по SSH-ключам; после проверки входа под `deploy`
отключите вход root по паролю (`PasswordAuthentication no`,
`PermitRootLogin prohibit-password` или `no` в `/etc/ssh/sshd_config`).
Членство в группе `docker` фактически равно root-доступу — выдавайте его
только тем, кто администрирует сервер.

### 5.2. Код и секреты

```bash
install -d -o deploy -g deploy /opt/cozy
su - deploy
# Репозиторий приватный: на сервере нужен read-only deploy key
# (GitHub → репозиторий → Settings → Deploy keys) или HTTPS-токен.
ssh-keygen -t ed25519 -f ~/.ssh/cozy_repo -N "" -C "cozy-vps-readonly"
cat ~/.ssh/cozy_repo.pub        # добавить в Deploy keys репозитория (без write access)
cat >> ~/.ssh/config <<'EOF'
Host github.com
  IdentityFile ~/.ssh/cozy_repo
  IdentitiesOnly yes
EOF
git clone git@github.com:<организация>/cozy_backend.git /opt/cozy
cd /opt/cozy

cp .env.example .env && chmod 600 .env
nano .env                       # заполнить по разделу 4

mkdir -p secrets && chmod 755 secrets
# скопировать JSON-ключ Firebase с рабочей машины:
#   scp firebase-service-account.json deploy@<IP>:/opt/cozy/secrets/
chmod 644 secrets/firebase-service-account.json
```

`secrets/` и `.env` в `.gitignore`; `deploy.sh` делает `git reset --hard`, но
неотслеживаемые файлы не трогает.

### 5.3. DNS

В DNS-зоне домена (сейчас `cozy.kg` обслуживается Cloudflare):

| Тип | Имя | Значение |
|---|---|---|
| A | `cozy.kg` (`@`) | IP prod-VPS |
| A | `www` | IP prod-VPS |
| A | `media` | IP prod-VPS |

Проще всего — режим «DNS only» (серое облако): Caddy сам получит сертификаты.
Если включаете проксирование Cloudflare (оранжевое облако) — SSL/TLS mode
строго **Full (strict)**, иначе будет бесконечный редирект; учтите лимит
Cloudflare на размер загрузки (100 МБ на бесплатном тарифе — для фото и
xlsx хватает).

Дождитесь, пока `dig +short cozy.kg` и `dig +short media.cozy.kg` вернут IP
сервера — иначе Caddy не сможет выпустить сертификат.

### 5.4. Caddyfile для prod

Готовый файл — `docker/Caddyfile.prod`; при `DEPLOY_ENV=production` compose
монтирует именно его (`CADDYFILE=Caddyfile.prod`). Хосты в нём не зашиты:
Caddy берёт их из `.env` (контейнер caddy получает его через `env_file`):

- `{$SITE_HOST}` — сайт, API, админка, вебхук Bakai (заголовок
  `X-Webhook-Token` ставится так же, как на staging);
- `www.{$SITE_HOST}` — постоянный редирект на `https://SITE_HOST`;
- `{$MEDIA_HOST}` — фото из MinIO, только GET/HEAD.

Отличия от staging: нет `noindex`, нет резервного блока по IP. Сниппеты
`security_headers`/`backend_proxy` совпадают с `docker/Caddyfile` — правьте
оба файла синхронно. `deploy.sh` проверяет файл (`caddy validate` с
переменными из `.env`) при первом prod-деплое и при каждом его изменении.
Проверить вручную:

```bash
docker run --rm --env-file .env -v "$PWD/docker/Caddyfile.prod:/etc/caddy/Caddyfile:ro" caddy:2.11.4-alpine \
  caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile
```

### 5.5. Первый запуск

`deploy.sh` подходит и для первого запуска: соберёт образ, поднимет
Postgres, применит все миграции на пустую базу, запустит остальные сервисы и
дождётся `/readyz` (`https://$SITE_HOST/readyz`). Обычно первый запуск —
это просто первый прогон workflow «Deploy production» (раздел 7.3); вручную:

```bash
cd /opt/cozy
DEPLOY_ENV=production DEPLOY_REF=v1.0.0 bash scripts/deploy.sh   # тег или SHA из main
```

Проверка:

```bash
$C ps                                   # 4 контейнера Up, postgres (healthy)
curl -fsS -o /dev/null -w '%{http_code}\n' https://cozy.kg/healthz   # 200 (тело пустое)
curl -fsS https://cozy.kg/readyz        # {"status":"ok","checks":{"db":"ok","minio":"ok"}}
bash scripts/migrate.sh version         # номер последнего файла migrations/*.up.sql, без "(dirty)"
$C logs --tail=50 backend               # нет ошибок; "push: FCM enabled", Telegram enabled
bash scripts/smoke.sh https://cozy.kg   # с рабочей машины: только GET, все PASS
```

### 5.6. Первый владелец админки

Экрана регистрации нет: сотрудников создаёт владелец в разделе «Сотрудники»,
а самого первого владельца создаёт команда `create-owner` того же бинарника
сервера (она же — восстановление доступа, если владелец забыл пароль):

```bash
cd /opt/cozy
read -rs -p 'Пароль владельца: ' PW; echo
printf '%s\n' "$PW" | $C exec -T backend /app/server create-owner --phone '+996XXXXXXXXX' --name 'Имя Владельца'
unset PW
```

- Пароль читается **только из stdin** (первая строка) — его нет ни в
  аргументах (`ps`), ни в истории shell, команда его не печатает. От 8
  символов, не длиннее 72 байт.
- Телефон — в любом формате КР (`0700 123 456`, `996700123456`,
  `+996700123456`), сохраняется как `+996XXXXXXXXX`; на странице входа его
  тоже можно вводить в любом из этих форматов.
- Хеш — bcrypt, ровно как при создании сотрудника в админке.
- Повторный запуск безопасен: если владелец с этим номером уже есть — ему
  ставится новый пароль, аккаунт активируется, все его сессии
  завершаются (`--name` при этом игнорируется). Если номер принадлежит
  менеджеру или сотруднику точки — команда откажет (`not_owner`): роль
  меняет владелец в админке.
- Вывод: `create-owner: created owner <id> (phone +996…)` или
  `reset password of owner <id> …`; код выхода не 0 при ошибке.

Затем войти на `https://cozy.kg/admin/login` и при желании сменить пароль
(«Сотрудники» → «Сменить пароль»).

### 5.7. Подключение внешних сервисов

1. **Bakai**: в кабинете мерчанта зарегистрировать вебхук
   `https://cozy.kg/api/v1/payments/bakai/webhook` (без `?token=`). Проверить
   пробной оплатой на небольшую сумму и возвратом.
2. **Nikita**: проверить вход в приложение/на сайт по реальному номеру.
3. **Firebase**: войти в приложение, сменить статус тестового заказа в
   админке — должен прийти push.
4. **Telegram**: оформить тестовый заказ — в группу персонала придёт сообщение
   со ссылкой на `/admin/orders/<id>`.
5. **Мобильное приложение**: собрать с
   `--dart-define=COZY_API_BASE_URL=https://cozy.kg/api/v1`
   (см. `cozy_mobile/docs/build-and-release.md`).
6. Настроить бэкапы (раздел 8) и мониторинг (раздел 9).

---

## 6. Миграции базы данных

- Файлы — `migrations/NNNNNN_*.up.sql` / `.down.sql`, инструмент —
  golang-migrate `v4.18.3` (образ `migrate/migrate:v4.18.3`), состояние — таблица
  `schema_migrations`.
- При деплое применяются автоматически (`deploy.sh` → `scripts/migrate.sh up`).
  Если миграция упала, деплой останавливается, старая версия продолжает
  работать.
- Вручную на сервере:

```bash
bash scripts/migrate.sh version     # текущая версия; "(dirty)" — миграция упала посередине
bash scripts/migrate.sh up          # применить новые
bash scripts/migrate.sh force 22    # отметить версию 22 как применённую, ничего не выполняя
bash scripts/migrate.sh down 1      # откатить последнюю (может удалить данные!)
```

- Правило для разработчиков: миграция должна быть совместима с кодом
  предыдущего релиза (сначала добавить колонку/таблицу, удалять старое —
  релизом позже). Откат кода (раздел 7.2) миграции **не** откатывает.
- Восстановление после `dirty` и после потери `schema_migrations` — README,
  раздел «Миграции: dirty и восстановление».
- Локально: `make migrate-up`, `make migrate-version`, `make migrate-force version=N`,
  новая миграция — `make migrate-new name=...`.

---

## 7. Деплой и откат

### 7.1. Как устроен деплой (`scripts/deploy.sh`)

Запускается на VPS из `/opt/cozy`. Шаги (старая версия обслуживает
запросы до шага 6):

0. проверки: есть env-файл, `git`, `docker compose`, `curl`; для prod — `DEPLOY_REF`, `SITE_HOST`, `MEDIA_HOST`, `APP_ENV=prod`; одновременно идёт только один деплой (`flock`);
1. `git fetch` и `git reset --hard` на `DEPLOY_REF` (staging: по умолчанию `origin/main`; prod: обязателен, подтягиваются теги);
2. если менялся Caddyfile окружения (`docker/Caddyfile` / `docker/Caddyfile.prod`; на prod — и при первом запуске) — проверка `caddy validate`;
3. сборка образа `cozy-backend:<первые 12 символов SHA>` (prod: `cozy-backend-prod:…`); образ, на котором backend работал до деплоя, помечается `:previous`;
4. Postgres поднят и здоров; если миграции в состоянии `dirty` — стоп с инструкцией;
5. применение миграций;
6. переключение на новый образ (`up -d`), перезапуск Caddy, если менялся его конфиг;
7. ожидание `HEALTH_URL` (до `HEALTH_TIMEOUT`=90 с). Не поднялся — логи и **автоматический откат** на `:previous`;
8. новый образ получает тег `:latest`; хранятся `KEEP_IMAGES`=5 последних образов.

Параметры (переменные окружения при запуске): `DEPLOY_ENV` (`staging` по
умолчанию / `production`, см. раздел 2), `DEPLOY_REF`, `ENV_FILE`,
`HEALTH_URL` (staging: `https://cozy.erpsystemsales.com/readyz`, prod:
`https://$SITE_HOST/readyz`), `HEALTH_TIMEOUT`, `KEEP_IMAGES`,
`MIGRATE_IMAGE`.

Ручной деплой:

```bash
# staging (как раньше):
ssh deploy@<staging-IP> 'cd /opt/cozy && bash scripts/deploy.sh'
# prod — тег или SHA обязателен:
ssh deploy@<prod-IP> 'cd /opt/cozy && DEPLOY_ENV=production DEPLOY_REF=v1.0.1 bash scripts/deploy.sh'
```

### 7.2. Откат

Код (на предыдущий или любой из сохранённых образов):

```bash
cd /opt/cozy
docker image ls cozy-backend-prod     # previous, latest и <sha12> последних деплоев (staging: cozy-backend)
BACKEND_TAG=previous $C up -d --no-build --no-deps backend
curl -fsS https://cozy.kg/readyz
```

Не откатывайтесь перезапуском старого workflow в GitHub Actions: старый
коммит не знает новых миграций, и `migrate up` завершится ошибкой.

Если вместе с кодом нужно вернуть данные — восстановление из дампа
(раздел 8.4). Это откатывает все заказы и изменения после момента дампа.

### 7.3. Автодеплой через GitHub Actions

- `.github/workflows/ci.yml` — на каждый push/PR: `gofmt`, `go vet`, `go test`,
  `golangci-lint`, миграции up/down/up на Postgres 16, интеграционные тесты.
- `.github/workflows/deploy.yml` — на каждый push в `main`: весь CI, затем по SSH
  запуск `scripts/deploy.sh` на сервере для именно этого коммита. Сейчас
  настроен на **staging** (`environment: staging`, URL staging).
- Секреты репозитория (Settings → Secrets and variables → Actions):

| Секрет | Значение |
|---|---|
| `DEPLOY_HOST` | IP/имя сервера |
| `DEPLOY_USER` | `deploy` |
| `DEPLOY_SSH_KEY` | приватный ключ отдельной пары ed25519 только для GitHub; публичная часть — в `~deploy/.ssh/authorized_keys` |
| `DEPLOY_SSH_FINGERPRINT` | отпечаток host-ключа сервера (обязателен, без него джоба падает): на VPS `ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub \| awk '{print $2}'` |
| `DEPLOY_PATH` | `/opt/cozy` |
| `DEPLOY_SSH_PORT` | необязательно, по умолчанию 22 |

- `.github/workflows/deploy-prod.yml` — **prod, только вручную**: Actions →
  «Deploy production» → Run workflow → поле `ref` (тег, например `v1.0.0`,
  или SHA). Сам по себе никогда не запускается. Шаги: ref → точный SHA
  (обязан быть в истории `main`) → весь CI на этом SHA → ожидание
  подтверждения в GitHub Environment `production` → по SSH
  `DEPLOY_ENV=production DEPLOY_REF=<sha> scripts/deploy.sh` на prod-сервере.
- Настройка один раз: Settings → Environments → New environment
  `production` → Required reviewers (кто подтверждает выкладку), Deployment
  branches and tags — по желанию. Секреты prod задаются **в этом
  Environment** (не на уровне репозитория), чтобы их видела только
  подтверждённая джоба:

| Секрет (Environment `production`) | Значение |
|---|---|
| `PROD_DEPLOY_HOST` | IP/имя prod-сервера |
| `PROD_DEPLOY_USER` | `deploy` |
| `PROD_DEPLOY_SSH_KEY` | приватный ключ отдельной пары ed25519 только для prod-деплоя |
| `PROD_DEPLOY_SSH_FINGERPRINT` | отпечаток ECDSA host-ключа prod-сервера (обязателен) |
| `PROD_DEPLOY_PATH` | `/opt/cozy` |
| `PROD_DEPLOY_SSH_PORT` | необязательно, по умолчанию 22 |

Выпуск релиза: `git tag -a v1.0.0 -m "..." <sha в main> && git push origin v1.0.0`,
затем запустить «Deploy production» с `ref=v1.0.0`. Откат кода — раздел 7.2
(или запуск workflow с предыдущим тегом, если новых миграций не было).

Остановить автодеплой staging на время (например, на демо):
`gh workflow disable Deploy`, вернуть — `gh workflow enable Deploy`.

---

## 8. Бэкапы и восстановление

### 8.1. Что делает `scripts/backup.sh`

1. `pg_dump -Fc` базы → `$BACKUP_DIR/postgres/cozy_ГГГГММДД_ЧЧММСС.dump`
   (сначала `.partial`, проверка `pg_restore --list`, затем переименование);
2. удаляет локальные дампы старше `KEEP_DAYS` (только после успешного шага 1);
3. зеркалирует бакет MinIO в `$BACKUP_DIR/minio/<bucket>/` (`mc mirror`,
   инкрементально; удалённые в MinIO файлы в зеркале остаются);
4. если задан `BACKUP_RCLONE_REMOTE` — копирует дампы и зеркало медиа за
   пределы VPS (`rclone copy`, не `sync`); off-site дампы старше
   `BACKUP_RCLONE_KEEP_DAYS` удаляются;
5. если задан `BACKUP_HEALTHCHECK_URL` — пингует мониторинг: `/start`, успех,
   `/fail` с хвостом лога.

Параметры (переменные окружения при запуске):

| Переменная | По умолчанию | Описание |
|---|---|---|
| `BACKUP_DIR` | `/var/backups/cozy` | куда складывать |
| `BACKUP_LOG` | `$BACKUP_DIR/backup.log` | лог |
| `KEEP_DAYS` | `14` | сколько дней хранить локальные дампы |
| `MINIO_MIRROR` | `1` | `0` — не зеркалировать медиа |
| `MINIO_BUCKET` | из `.env`, иначе `cozy-media` | бакет |
| `MC_IMAGE` | `quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z` | образ `mc` |
| `BACKUP_RCLONE_REMOTE` | — (нет off-site копии) | например `b2:cozy-backups/prod` |
| `BACKUP_RCLONE_KEEP_DAYS` | `60` | хранение off-site дампов; `0` — бессрочно |
| `RCLONE_CONFIG` | `~/.config/rclone/rclone.conf` | конфиг rclone |
| `RCLONE_IMAGE` | `rclone/rclone:1.75.1` | если `rclone` не установлен на хосте |
| `BACKUP_HEALTHCHECK_URL` | — | например `https://hc-ping.com/<uuid>` |

Код выхода ненулевой при любой ошибке, ошибки дублируются в stderr.

### 8.2. Настройка off-site копии (один раз)

Бэкап на том же диске, что и база, не переживёт потерю VPS. Off-site копия
обязательна для prod.

1. Завести бакет в Backblaze B2 / любом S3-хранилище **на аккаунт заказчика**,
   создать ключ доступа только к этому бакету.
2. На VPS: `apt install rclone`, затем `rclone config` → новый remote
   (например `b2`). Проверка: `rclone lsd b2:`.
3. Пробный запуск:
   `BACKUP_RCLONE_REMOTE=b2:cozy-backups/prod bash /opt/cozy/scripts/backup.sh`
   и `rclone ls b2:cozy-backups/prod/postgres | tail`.

### 8.3. Расписание (cron)

Скрипт **сам себя не планирует** — cron нужно добавить на сервере вручную
(`crontab -e` под root или под пользователем с доступом к docker; если под
`deploy` — `BACKUP_DIR` должен быть доступен ему на запись):

```cron
MAILTO=admin@example.kg
30 3 * * * DEPLOY_ENV=production BACKUP_HEALTHCHECK_URL=https://hc-ping.com/<uuid> BACKUP_RCLONE_REMOTE=b2:cozy-backups/prod KEEP_DAYS=14 /opt/cozy/scripts/backup.sh >/dev/null
```

Каждую ночь в 03:30 по времени сервера. `DEPLOY_ENV=production` в строке
cron обязателен на prod (cron не читает `~/.profile`); на staging его нет. stdout отбрасывается (он уже в
`backup.log`), ошибки cron отправит на `MAILTO` (если на сервере настроена
почта); основной алерт — healthchecks.io (период 1 день, grace 2 часа).

Проверить, что расписание есть: `crontab -l | grep backup.sh`; что бэкапы
идут: `ls -lt /var/backups/cozy/postgres | head` и `tail /var/backups/cozy/backup.log`.

### 8.4. Восстановление Postgres

Перезаписывает текущие данные — сначала сделайте свежий дамп текущего
состояния (`bash scripts/backup.sh`).

```bash
cd /opt/cozy
C="docker compose -f docker/docker-compose.prod.yml --env-file .env"
# если VPS потерян — сначала забрать дамп из off-site:
#   rclone copy b2:cozy-backups/prod/postgres/cozy_YYYYMMDD_HHMMSS.dump /var/backups/cozy/postgres/
$C stop backend                                   # никто не пишет в базу
$C exec -T postgres dropdb -U cozy cozy
$C exec -T postgres createdb -U cozy cozy
$C exec -T postgres pg_restore -U cozy -d cozy --no-owner --no-acl --exit-on-error \
  < /var/backups/cozy/postgres/cozy_YYYYMMDD_HHMMSS.dump
bash scripts/migrate.sh version                   # версия из дампа, без "(dirty)"
bash scripts/migrate.sh up                        # докатить миграции новее дампа
$C start backend
curl -fsS https://cozy.kg/readyz
```

Проверка дампа без восстановления: `pg_restore --list <файл>`. Пробное
восстановление в отдельную базу (делать хотя бы раз в месяц):

```bash
$C exec -T postgres createdb -U cozy cozy_check
$C exec -T postgres pg_restore -U cozy -d cozy_check --no-owner --no-acl < /var/backups/cozy/postgres/<файл>.dump
$C exec -T postgres psql -U cozy -d cozy_check -c 'SELECT count(*) FROM orders;'
$C exec -T postgres dropdb -U cozy cozy_check
```

### 8.5. Восстановление фото (MinIO)

```bash
cd /opt/cozy
# если VPS потерян: rclone copy b2:cozy-backups/prod/minio /var/backups/cozy/minio
set -a; . ./.env; set +a       # MINIO_ACCESS_KEY / MINIO_SECRET_KEY / MINIO_BUCKET в окружение
MINIO_ID=$($C ps -q minio)
MC_HOST_cozy="http://$MINIO_ACCESS_KEY:$MINIO_SECRET_KEY@localhost:9000" \
  docker run --rm --network container:$MINIO_ID -e MC_HOST_cozy -v /var/backups/cozy/minio:/backup \
  quay.io/minio/mc:RELEASE.2025-08-13T08-35-41Z mirror --overwrite /backup/cozy-media cozy/cozy-media
```

(Замените `cozy-media` на значение `MINIO_BUCKET`, если оно другое.
`. ./.env` выполняет файл как shell-скрипт — убедитесь, что в нём нет
значений с пробелами без кавычек.)

### 8.6. Полная потеря сервера

1. Новый VPS по разделам 5.1–5.4 (тот же `.env` и `secrets/` из менеджера
   паролей заказчика, DNS перевести на новый IP).
2. `bash scripts/deploy.sh` с `HEALTH_URL` — поднимет пустую базу и MinIO.
3. Восстановить базу (8.4) и фото (8.5) из off-site копии.
4. Проверить вход, каталог с фото, тестовый заказ.

---

## 9. Мониторинг и диагностика

- **Health-эндпоинты**: `GET /healthz` — процесс жив (200, пустое тело); `GET /readyz` —
  готовность: пинг Postgres и `GET /minio/health/live`, таймаут 2 с на
  проверку; при сбое 503 и JSON вида
  `{"status":"unavailable","checks":{"db":"ok","minio":"error"}}`.
- **Внешний мониторинг доступности** (рекомендуется): любой uptime-сервис
  (UptimeRobot, healthchecks.io, Better Stack) с проверкой
  `https://cozy.kg/readyz` раз в 1–5 минут и уведомлением в Telegram/e-mail.
- **Мониторинг бэкапов**: `BACKUP_HEALTHCHECK_URL` (раздел 8.3).
- **Логи**: `$C logs --tail=200 backend` (также `postgres`, `minio`, `caddy`).
  Каждый запрос имеет `X-Request-ID` — он есть в ответе, в JSON ошибок и в
  каждой строке лога: `$C logs backend | grep <request-id>`. Логи контейнеров
  ротируются (5 файлов × 10 МБ на контейнер).
- **Диск**: `df -h /`, `docker system df`. Старые образы чистит `deploy.sh`;
  дампы — `backup.sh` по `KEEP_DAYS`.
- **Сертификаты**: Caddy продлевает сам; проблемы — `$C logs caddy`.
- **Smoke-тест** после деплоя/отката: `bash scripts/smoke.sh https://cozy.kg`.
- Таблица «симптом → что смотреть → что делать» — [`runbook-demo.md`](runbook-demo.md), раздел 3.

---

## 10. Обслуживание

- **Обновление образов** Postgres/MinIO/Caddy — правкой тега в
  `docker/docker-compose.prod.yml` и деплоем. Минорные версии Postgres 16.x
  совместимы; переход на новую мажорную версию — только через
  `pg_dump`/`pg_restore`.
- **Смена секрета**: поправить `.env`, затем `$C up -d backend` (и `caddy`,
  если менялся `BAKAI_WEBHOOK_TOKEN`). Смена `JWT_SECRET` разлогинит
  покупателей в приложении; смена `MINIO_*` ключей требует пересоздать
  контейнер `minio` с теми же томами.
- **Смена пароля Postgres** в `.env` не меняет пароль в уже созданной базе:
  сначала `ALTER ROLE cozy PASSWORD '...'` в psql, потом `.env` и
  `$C up -d`.
- **Токен Bakai** не имеет срока и обновления: при ошибках 401 в логе
  перевыпустить у банка и заменить `BAKAI_API_TOKEN`.
