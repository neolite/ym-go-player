# yandex-music v3: OAuth Device Flow и Ynison — подтверждённые детали

Заметки по мотивам релиза [yandex-music v3](https://ym.marshal.dev/changes) (Python, MarshalX).
Детали подтверждены по исходникам [MarshalX/yandex-music-api](https://github.com/MarshalX/yandex-music-api)
(`yandex_music/_client/device_auth.py`, `yandex_music/ynison/…`) и докам
[ym.marshal.dev/token](https://ym.marshal.dev/token), [ym.marshal.dev/ynison](https://ym.marshal.dev/ynison).
Снято 2026-08-23. OAuth Device Flow **реализован** (`internal/auth/device.go`, `internal/httpapi/auth.go`, `web/src/app.ts`). Реализация Ynison в этом проекте **отложена** — документ фиксирует ресёрч на будущее.

Легенда: ✅ — подтверждено исходниками/доками; ⚠️ — предположение или типичное значение, в источнике не зафиксировано.

## 1. OAuth Device Flow

Вход без ручного копирования токена: библиотека/клиент получает код, пользователь подтверждает
его на странице Яндекса, клиент поллит до выдачи токена.

### Учётка (официальное Android-приложение Яндекс Музыки) ✅

```
client_id     = 23cabbbdc6cd418abb4b39c32c41195d   # тот же, что в internal/httpapi/auth.go (implicit-flow)
client_secret = 53bc75238f0c4d08a118e51fe9203300
```

### Шаг 1 — запрос device code ✅

`POST https://oauth.yandex.ru/device/code`, тело `application/x-www-form-urlencoded`:

- `client_id` — константа выше
- `device_id` — любая строка (либа генерит случайные 10 символов, если не задан)
- `device_name` — дефолт либы `YandexMusicAPI`

Ответ:

```json
{
  "device_code": "<opaque>",
  "user_code": "<код для пользователя>",
  "verification_url": "<страница ввода кода>",
  "expires_in": 300,
  "interval": 5
}
```

⚠️ `expires_in: 300` и `interval: 5` видны только в моках тестов либы — реальные значения приходят с сервера.

### Шаг 2 — поллинг токена ✅

`POST https://oauth.yandex.ru/token`, form-urlencoded:

- `grant_type=device_code`
- `code` = `device_code` из шага 1
- `client_id`, `client_secret` — константы

Исходы:

- успех: `{"access_token": "...", "refresh_token": "...", "expires_in": 31536000, "token_type": "bearer"}`
  (все поля, кроме access_token, опциональны в модели либы);
- ожидание: HTTP 400, в теле `authorization_pending` → продолжать поллинг;
- любая другая OAuth-ошибка (`expired_token`, `access_denied`, `invalid_client`, …) → стоп.

### Семантика поллинга ✅

- Интервал — `interval` из шага 1 (если вызывающий не переопределил).
- Общий дедлайн — `expires_in` из шага 1.
- Цикл: poll → sleep(interval) → poll … до токена или дедлайна.
- ⚠️ `slow_down` либа не обрабатывает — в своей реализации стоит учесть.

### Хранение токена ✅

Библиотека **не** сохраняет токен и **не** обновляет его по `expires_in` — ответственность
вызывающего кода. `grant_type=refresh_token` в либе нигде не используется.

## 2. Ynison (BETA)

WebSocket-протокол синхронизации состояния между устройствами («текущий трек», список
устройств, удалённое управление). Двухшаговое подключение, JSON-текстовые фреймы.

### Шаг 1 — redirect service ✅

WS `wss://ynison.music.yandex.ru/redirector.YnisonRedirectService/GetRedirectToYnison`

- Заголовки (оба обязательны): `Origin: https://music.yandex.ru`, `Authorization: OAuth <token>`.
- Подпротоколы (`Sec-WebSocket-Protocol`): `["Bearer", "v2", <urlencoded JSON>]`, где JSON:

```json
{"Ynison-Device-Id": "<device_id>", "Ynison-Device-Info": "{\"app_name\":\"...\",\"type\":\"1\"}"}
```

  - ⚠️ `Ynison-Device-Info` — **вложенная JSON-строка** (двойное кодирование), без этого сервер отвергает подключение.
  - `type: "1"` = браузер/WEB.
  - `device_id` обязан быть **стабильным между запусками** — иначе в аккаунте плодятся устройства.
    Дефолт либы: `hex(int(1e16 * random()))`.

Ответ — один текстовый фрейм, после чего redirect-сокет закрывается:

```json
{
  "host": "<персональный ynison-хост>",
  "redirectTicket": "<anti-DDoS тикет>",
  "sessionId": 123,
  "keepAliveParams": {"keepAliveTimeSeconds": 0, "keepAliveTimeoutSeconds": 0}
}
```

### Шаг 2 — state service ✅

WS `wss://{host}/ynison_state.YnisonStateService/PutYnisonState`

Те же заголовки и подпротоколы, но JSON в подпротоколе дополнен:

```json
{"...": "...", "Ynison-Redirect-Ticket": "<redirectTicket>", "Ynison-Session-Id": "<sessionId строкой>"}
```

Сразу после подключения клиент шлёт `updateFullState` — регистрацию. Чтобы быть наблюдателем,
а не плеером: `can_be_player=false`, `can_be_remote_controller=true`, пустая очередь
(`current_playable_index=-1`), `paused=true`, `is_currently_active=false`, свежий `rid` (uuid4),
`activity_interception_type=DO_NOT_INTERCEPT_BY_DEFAULT(0)`.

Дальше сервер пушит `PutYnisonStateResponse` на каждую смену состояния:

- текущий трек: `player_state.player_queue.playable_list[current_playable_index]`
  (`playable_id` = id трека, `title`, `cover_url_optional`, `album_id_optional`);
- пауза/прогресс: `player_state.status` (`progress_ms`, `duration_ms`, `paused`, …);
- устройства: `devices[]` (`info{device_id, title, type, app_name}`, `volume_info`, `is_offline`),
  активное — `active_device_id_optional`.

⚠️ Имена полей на проводе — camelCase proto-JSON (`updateFullState`, `currentPlayableIndex`, …):
дефолт `betterproto`, либа его не переопределяет. Нулевые/дефолтные поля опускаются.

### Keepalive и реконнект ✅

- WS ping каждые 20 с (захардкожено; `keepAliveParams` из redirect-фрейма парсятся, но не используются).
- Обрыв → полный цикл с шага 1 (тикет/сессия не продлеваются — перевыпрашиваются).
- Backoff: 2^n с ± 0.5 jitter, потолок 64 с; на реконнекте `updateFullState` шлётся заново.

### Статус и лаги ✅/⚠️

- Модуль в BETA: нестабильные соединения, API может меняться без semver.
- Подключение медленное; автор либы добился ~0.5 с медианы в своих экспериментах
  ([пост в Telegram](https://t.me/yandex_music_api/79131)).

## 3. Что уже есть в проекте под будущую реализацию

- `gorilla/websocket` уже в `go.sum` (indirect через Wails) — перевести в прямые при использовании.
- Авторизация сегодня — ручной ввод токена: `internal/httpapi/auth.go` (роуты `/api/auth/*`),
  `web/src/app.ts` (панель `#auth`, `extractToken`).
- Токен хранится в keyring: `internal/auth/store.go` (service `music212`, user `yandex-music-oauth`);
  стабильный Ynison device id логично хранить там же отдельной записью.
- Состояние на фронтенд уже стримится по SSE: `internal/httpapi/sse.go` (`GET /api/events`) —
  точка расширения для публикации ynison-состояния.
