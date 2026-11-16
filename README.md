# Session

Небольшой сервис авторизации на Go: JSON API и встроенный веб-интерфейс для управления сессиями.

- Регистрация и вход по email и паролю.
- Профиль, список активных сессий, отзыв одной сессии и выход со всех устройств.
- PostgreSQL хранит пользователей, Redis — серверные сессии и лимиты попыток.
- HttpOnly cookie, CSRF-защита, хеширование паролей, абсолютный TTL и не более 10 сессий.
- Один Go-бинарник, без отдельной сборки фронтенда.

## Запуск через Docker

Нужны Docker Desktop с Linux containers и Docker Compose.

1. Если `.env` ещё нет, скопировать `.env.example` в `.env` и задать `DB_PASSWORD`:

   ```powershell
   Copy-Item .env.example .env
   ```

2. Запустить приложение:

   ```powershell
   docker compose up -d --build
   ```

3. Открыть [http://localhost:8585](http://localhost:8585) и создать свой аккаунт.

Проверка готовности: [http://localhost:8585/ready](http://localhost:8585/ready). Порт меняется через `APP_EXTERNAL_PORT`. Compose всегда запускает приложение на внутреннем порту 8080, независимо от `APP_PORT` для нативного запуска.

```powershell
docker compose ps
docker compose logs -f app
docker compose down
```

`down` сохраняет данные в volumes. Миграции применяются автоматически при старте приложения к новому и существующему PostgreSQL; удалять volume для обновления схемы не нужно. Если volume уже существовал, использовать соответствующие ему DB_USER/DB_PASSWORD/DB_NAME: переменные PostgreSQL не меняют пароль существующей базы.

## Локальная разработка

Нужен Go 1.27+ и запущенные зависимости.

Интерфейс будет доступен на [http://localhost:8080](http://localhost:8080), если `APP_PORT` в `.env` не переопределён.

| Переменная | По умолчанию | Назначение |
| --- | --- | --- |
| APP_PORT | 8080 | Порт нативного Go-процесса |
| DB_HOST / DB_PORT | localhost / 5532 | PostgreSQL для нативного запуска |
| DB_USER / DB_NAME | auth / authdb | Пользователь и база |
| DB_PASSWORD | — | Пароль базы; обязателен в Compose |
| DB_SSLMODE | disable | Режим TLS PostgreSQL |
| REDIS_ADDR | localhost:56379 | Redis для нативного запуска |
| REDIS_PASSWORD / REDIS_DB | пусто / 0 | Параметры Redis |
| SESSION_TTL | 24h | Абсолютный срок сессии; 1m–720h |
| COOKIE_SECURE | false | true при использовании HTTPS |
| APP_EXTERNAL_PORT | 8585 | Порт Docker-приложения на localhost |
| POSTGRES_EXTERNAL_PORT | 5532 | Локальный порт PostgreSQL |
| REDIS_EXTERNAL_PORT | 56379 | Локальный порт Redis |


## API

| Метод | URL | Авторизация |
| --- | --- | --- |
| POST | /api/auth/register | Нет; создаёт аккаунт и сессию |
| POST | /api/auth/login | Нет; создаёт сессию |
| POST | /api/auth/logout | Идемпотентный выход |
| GET | /api/me | Cookie |
| GET | /api/sessions | Cookie |
| DELETE | /api/sessions/{id} | Cookie, только собственная сессия |
| POST | /api/auth/logout-all | Cookie; завершает и текущую сессию |
| GET | /health | Нет |
| GET | /ready | Нет |

Регистрация/вход: `Content-Type: application/json`, тело `{"email":"you@example.com","password":"a long unique password"}`. Регистрация требует пароль длиной 12–1024. Email нормализуется; пароль сохраняет все пробелы. Неизвестные JSON-поля запрещены.

Успешные register/login/me возвращают `user` и `session`, список — `{"sessions":[...]}`. Logout/revoke возвращают 204. Токен передаётся только через Set-Cookie, в JSON его нет. ID сессии из списка не является токеном входа.

Ошибки: `{"error":{"code":"invalid_credentials","message":"Неверный email или пароль."}}`. Статусы: 400 валидация, 401 нет доступа/неверные данные, 403 CSRF, 404 чужая или отсутствующая сессия, 409 занятый email, 413 слишком большое тело, 415 неверный Content-Type, 429 лимит, 503 зависимость недоступна. Лимиты на один IP за 15 минут: 5 регистраций и 20 попыток входа; учитываются и успешные попытки. При 429 возвращается `Retry-After` в секундах.

## Ограничения

Это завершённое небольшое приложение для локального использования и дальнейшего развития. Восстановление пароля, проверка email и MFA не реализованы. Если Redis потеряет данные, пользователи войдут повторно. Регистрация фиксирует пользователя до создания сессии: если Redis откажет между этими шагами, аккаунт останется, и после восстановления Redis нужно воспользоваться входом. Redis Lua-операции рассчитаны на один инстанс, а не Redis Cluster.
