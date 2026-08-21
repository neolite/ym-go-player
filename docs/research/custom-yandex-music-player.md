# Кастомный легковесный плеер Яндекс Музыки — исследование

> Дата исследования: 2026-08-20. Все данные получены из первичных источников (GitHub-репозитории, их README/исходники, документация MarshalX). Публичного официального API у Яндекс Музыки **нет** — всё описанное ниже основано на реверс-инжиниринге и может сломаться в любой момент.
>
> Обозначения достоверности:
> - ✅ — подтверждено первичным источником (исходник/README/офиц. док).
> - ⚠️ — из вторичного описания или поиска, **не** проверено по исходнику напрямую.

---

## TL;DR — какой путь выбрать

**Рекомендация: Вариант C с элементами A — локальный демон на Python-библиотеке `MarshalX/yandex-music-api` + тонкий фронтенд.**

Причины:
1. **`MarshalX/yandex-music-api` — единственная по-настоящему живая и полная библиотека** (последний push 2026-04-19, 1248★, LGPL-3.0). Она покрывает всё, что нужно минимальному плееру: аккаунт, поиск, плейлисты, треки, `download-info`, ротор («Моя волна»), фидбек проигрывания. ✅
2. **Прямой web/browser-клиент на чистом JS невозможен без прокси**: у API нет CORS-заголовков (отсюда существование `acherkashin/yandex-music-cors-proxy`). ✅ Значит, всё равно нужен серверный/десктопный слой — Python-демон или Rust/Tauri backend.
3. **Аутентификацию делаем через готовый OAuth-токен** (client_id `23cabbbdc6cd418abb4b39c32c41195d`), вводимый пользователем. Свой OAuth-app создать нельзя. ✅
4. **Плюс-подписка обязательна** для полноценного стриминга и для FLAC. ✅

Если цель — **готовый десктоп с минимумом работы**, а не свой UI — берите не форк, а **`TheKing-OfTime/YandexMusicModClient` → его преемник PulseSync** (мод официального Electron-клиента). Но учтите: MOD-клиент **заархивирован 2026-02-09** в пользу слияния с PulseSync. ✅

Если нужен **headless / MPD-подобный сценарий** — `trudenboy/ma-provider-yandex-music` (провайдер для Music Assistant, живой, FLAC + Моя волна + lyrics, push 2026-08-11). ✅

> ⚠️ **Важное предупреждение по стримингу:** старая схема прямых ссылок (`get-mp3` + MD5-соль `XGRlBW9FXlekgbPrRHuSiA`) продолжает работать только для устаревших MP3/AAC-качеств. Для высоких качеств и **FLAC** Яндекс перешёл на новый эндпоинт `get-file-info` с подписью **HMAC-SHA256** (ключ `7tvSmFbyf5hJnIHhCimDDD`). Планируйте под новую схему. ✅ (ключ/эндпоинт — из README-описаний загрузчиков; см. §3, помечено уровнем доверия).

---

## 1. Ландшафт неофициального API

### 1.1 MarshalX/yandex-music-api (Python) — опорная библиотека экосистемы

- **Статус/свежесть:** активна. `pushed_at = 2026-04-19`, 1248★, 28 открытых issues, лицензия **LGPL-3.0**. ✅ (GitHub API)
- **Природа:** «неофициальная Python-библиотека», построена реверс-инжинирингом недокументированного API. Явный дисклеймер: *«⚠️ Это неофициальная библиотека»*. ✅
- **Возможности:** поиск, треки по ID, плейлисты (включая «Мне нравится»/Likes), скачивание (`download()`), данные аккаунта; sync + async (asyncio, опционально `aiohttp`/`aiofiles`); Python 3.8+. ✅
- **Авторизация:** два пути — (1) OAuth Device Flow прямо из библиотеки (`client.device_auth(on_code=...)` → `OAuthToken` с `access_token/refresh_token/expires_in/token_type`); (2) передача готового токена в конструктор `Client`. **Хранение и обновление токена — ответственность вызывающего кода**, библиотека не сохраняет и не рефрешит токен. ✅
- **Ограничение без авторизации:** доступны только первые ~30 секунд трека («Работа без авторизации ограничена»). ✅
- **Документация:** `yandex-music.readthedocs.io` редиректит на `ym.marshal.dev` (актуальное зеркало доков). ✅

**Как работает `get_download_info` / прямые ссылки (старая MP3-схема):** ✅ (подтверждено по исходнику `download_info.py`)
- `DownloadInfo` содержит поля: `codec`, `bitrate_in_kbps`, `direct`, `download_info_url` (ссылка на XML-документ с данными для загрузки).
- `get_direct_link()`:
  1. Забирает XML по `download_info_url`, извлекает узлы `host`, `path`, `ts`, `s`.
  2. Считает подпись:
     `sign = md5(("XGRlBW9FXlekgbPrRHuSiA" + path[1:] + s).encode('UTF-8')).hexdigest()`
  3. Итоговый URL: `https://{host}/get-mp3/{sign}/{ts}{path}`
- **Время жизни:** ссылка `download_info_url`/direct-link валидна **~1 минуту** — после этого 410 ошибка. ✅
- Соль `XGRlBW9FXlekgbPrRHuSiA` — жёстко зашита в исходнике. ✅

**Известные поломки 2024–2026:** ⚠️
- Старая схема `get-mp3`/`download-info` перестала выдавать высокие качества и lossless — под FLAC требуется новая схема `get-file-info` (см. §3). Подтверждается тем, что отдельные загрузчики (`llistochek`, `Stmol`) добавляли поддержку «newer lossless responses» и меняли авторизацию (v3: только `--token`, cookie-авторизация убрана). ✅ (MIGRATION.md llistochek)
- Прямого свидетельства, что именно `MarshalX` сломался целиком, я не нашёл — библиотека жива и обновляется. Не проверял её текущее покрытие FLAC по исходнику. ⚠️

Источники: <https://github.com/MarshalX/yandex-music-api>, <https://ym.marshal.dev/>, <https://ym.marshal.dev/en/latest/yandex_music.download_info.html>, исходник `download_info.py` (raw в main).

### 1.2 Другие библиотеки

| Проект | Язык | Свежесть (push) | ★ | Статус | Заметки |
|---|---|---|---|---|---|
| [MarshalX/yandex-music-api](https://github.com/MarshalX/yandex-music-api) | Python | 2026-04-19 | 1248 | ✅ живой | Эталон, самое полное покрытие |
| [K1llMan/Yandex.Music.Api](https://github.com/K1llMan/Yandex.Music.Api) | C# / .NET | 2026-04-19 | 108 | ✅ живой | Форк от Winster332, «отцеплен», переписан; перешёл с frontend- на **mobile**-эндпоинты ⚠️ |
| [Winster332/Yandex.Music.Api](https://github.com/Winster332/Yandex.Music.Api) | C# / .NET | 2022-12-08 | 61 | ⚠️ заброшен | Разделён на `Yandex.Music.Client` + `Yandex.Music.Api` |
| [acherkashin/yandex-music-open-api](https://github.com/acherkashin/yandex-music-open-api) | Swagger/OpenAPI (TS/JS, C# примеры) | 2023-04-02 | 91 | ⚠️ полузаброшен | **Swagger-спека** эндпоинтов + генерация клиента; ценен как справочник эндпоинтов; ссылается на MarshalX как первооснову |
| [vyfor/yandex-music-rs](https://github.com/vyfor/yandex-music-rs) | Rust | 2026-07-17 | 12 | ✅ живой (маленький) | «wrapper, только для образовательных целей»; свежий, но небольшой охват |
| [zhuravlevma/yandex-music-api](https://github.com/zhuravlevma/yandex-music-api) | Rust | — | — | ⚠️ не проверял push | crate `yandex-music-api`, MSRV 1.60 |
| [ndrewnee/go-yamusic](https://github.com/ndrewnee/go-yamusic) | Go | 2022-12-05 | 35 | ⚠️ заброшен | Порт node-библиотеки в стиле google/go-github |

Дополнительно — TS/JS: `acherkashin/yandex-music-extension` (VS Code) и `acherkashin/yandex-music-cors-proxy` (прокси для обхода отсутствия CORS). ✅ Само наличие CORS-прокси — прямое доказательство, что API **нельзя** дёргать из браузера напрямую.

Вывод по языкам: **Python (MarshalX) — самый зрелый; C# (K1llMan) — живой второй; Rust/Go — либо свежие но крошечные, либо заброшены.**

### 1.3 Ynison — WebSocket-протокол синхронизации между устройствами

- **Что это:** внутренний протокол Яндекса для синхронизации состояния воспроизведения между устройствами — «аналог Spotify Connect». Позволяет выступать «пультом ДУ» для официального приложения. ✅
- **Транспорт:** долгоживущий WebSocket (JSON over WebSocket по описанию Music Assistant-плагина; в других реализациях — protobuf). Реконнекты с экспоненциальным backoff. ✅ / состояние (текущий трек, очередь, play/pause/seek/next/prev, очередь «Моя волна»). ✅
- **Хендшейк:** ⚠️ (из вторичных описаний, **не** по исходнику)
  - Эндпоинт `wss://ynison.music.yandex.net` (и/или redirector-домен `ynison-redirector.music.yandex.net`); практикуется двухшаговая схема: сначала обращение к redirector, затем к выданному ynison-хосту.
  - Заголовки: `Ynison-Device-Id` (случайная строка ~16 символов), `Ynison-Device-Info` (JSON с `app_name`, `type`), `Sec-WebSocket-Protocol` с Bearer-токеном, OAuth-токен в `Authorization`.
- **Реализации:**
  - [trudenboy/ma-provider-yandex-ynison](https://github.com/trudenboy/ma-provider-yandex-ynison) — плагин «Music Connect» для Music Assistant (push 2026-08-18) ✅ живой
  - [bulatorr/go-ynisonbiostate](https://github.com/bulatorr/go-ynisonbiostate) (Go, использует пакет `go-yaynison`) ✅
  - [s-andrianov/ynison-cli-monitor](https://github.com/s-andrianov/ynison-cli-monitor), [s-andrianov/ym-ynsion-clients-sync](https://github.com/s-andrianov/ym-ynsion-clients-sync)
  - [Gatabarr/YMDRP](https://github.com/Gatabarr/YMDRP) — Discord Rich Presence на Rust через OAuth + Ynison (push 2026-08-16)
  - [Judd1zzz/yandex-music-streamdeck](https://github.com/Judd1zzz/yandex-music-streamdeck) — Python-версия имеет более полную Ynison-реализацию; в актуальной сборке Ynison-модуль на Rust — заглушка, а локальное управление идёт через **Chrome DevTools Protocol** (порт 9222), не через Ynison. ✅
- **Официальной документации по Ynison нет.** ✅

> Для *минимального плеера* Ynison **не обязателен** — он нужен только если вы хотите синхронизацию/роль «пульта» с официальным приложением. Для самостоятельного воспроизведения достаточно REST + `download-info`.

---

## 2. Авторизация

- **Основной способ на практике — implicit OAuth через готовый client_id Яндекс Музыки:**
  открыть `https://oauth.yandex.ru/authorize?response_type=token&client_id=23cabbbdc6cd418abb4b39c32c41195d`, залогиниться, забрать `access_token` из фрагмента URL после редиректа. ✅
- **Свой OAuth-app создать нельзя** — документация MarshalX прямо пишет: *«You cannot create your own OAuth application»*. Используем этот client_id или официальные клиенты. ✅
- **Экстракторы токена (готовые инструменты):** ✅
  - [MarshalX/yandex-music-token](https://github.com/MarshalX/yandex-music-token) — набор проектов для входа (web-app с device flow — работает для всех аккаунтов).
  - Браузерные расширения: [yandex-music-token для Firefox](https://addons.mozilla.org/en-US/firefox/addon/yandex-music-token/), [для Chrome](https://chromewebstore.google.com/detail/yandex-music-token/lcbjeookjibfhjjopieifgjnhlegmkib) — перехватывают токен из редиректа.
  - [Hazzz895/Get-Yandex-Music-Token](https://github.com/Hazzz895/Get-Yandex-Music-Token) — для Windows.
- **OAuth Device Flow** (в самой библиотеке MarshalX) — блокирующий вызов, ждёт подтверждения на странице Яндекса; работает для всех аккаунтов. ✅
- **Время жизни токена:** в примере `expires_in ≈ 31 535 645` сек (~1 год). ✅ Библиотека сам токен **не рефрешит**.
- **Captcha/2FA:** документация MarshalX по токену **не упоминает** отдельных проблем с captcha/2FA для implicit/device-flow (эти способы их обходят, т.к. логин идёт на странице Яндекса). ✅ Проблемы SmartCaptcha возникают при попытках логина по паролю программно — поэтому login/password-подход считается «нестабильным» (ср. с интеграцией `AlexxIT/YandexStation`, где password/one-time-password помечены «unstable», а рекомендованы QR-код/cookies/token). ✅
- **Блокирует ли Яндекс сторонние токены / изменения 2024–2026:** ⚠️
  - Прямых первичных свидетельств массовых блокировок именно за сторонний токен я **не нашёл**.
  - Косвенно: в v3 загрузчика `llistochek` **cookie-авторизация была полностью убрана** (`--cookies-path`, `--browser`, `--user-agent` больше не работают, только `--token`) — это указывает на ужесточение/изменение схемы авторизации со стороны Яндекса. ✅ (MIGRATION.md)

Источники: <https://ym.marshal.dev/token/>, <https://github.com/MarshalX/yandex-music-token>, <https://github.com/AlexxIT/YandexStation>, <https://github.com/llistochek/yandex-music-downloader/blob/master/MIGRATION.md>.

---

## 3. Механика стриминга / скачивания

### 3.1 Старая схема (MP3/AAC, до высоких качеств) — работает, но ограниченно ✅
- `GET .../tracks/{id}/download-info` → список `DownloadInfo` (`codec` = mp3/aac, `bitrate_in_kbps` = 64/128/192/320, `download_info_url`).
- По `download_info_url` — XML с `host/path/ts/s`.
- Подпись: `md5("XGRlBW9FXlekgbPrRHuSiA" + path[1:] + s)`; URL `https://{host}/get-mp3/{sign}/{ts}{path}`.
- Ссылка живёт ~1 минуту (410 после). ✅

### 3.2 Новая схема (высокое качество + FLAC lossless) ⚠️
Из README/описаний актуальных загрузчиков (не проверял построчно по исходнику):
- Эндпоинт **`get-file-info`** с подписью **HMAC-SHA256**.
- Подписываются данные `Timestamp + TrackID + Quality + Codec + Transport` ключом **`7tvSmFbyf5hJnIHhCimDDD`**, результат — base64 **без padding** (убираются `=`). ⚠️
- Качества (по `llistochek`): `0` — AAC 64 kbps, `1` — AAC 192 kbps, `2` — **FLAC (lossless)**. ✅
- FLAC-файлы могут приходить как `.flac` или `.m4a` в зависимости от ответа Яндекса; свежие загрузчики допиливали совместимость под «newer lossless responses». ✅ (Stmol/yandex-music-downloader)
- Отдельные проекты (`llistochek`) кредитуют внешних авторов за «метод дешифрования файлов» и «скрипт получения ссылки в lossless» — т.е. lossless может отдаваться в зашифрованном виде и требовать доп. расшифровки. ⚠️

> **Практический вывод:** для нового плеера закладывайте `get-file-info`+HMAC, а `get-mp3`/MD5 держите как fallback для низких качеств. Точные детали подписи проверьте по актуальному исходнику `llistochek/yandex-music-downloader` перед реализацией.

### 3.3 Подписка Plus и HLS ✅ / ⚠️
- **Стриминг требует активной Plus-подписки.** Без Plus — воспроизведение падает на «highest quality available», а **FLAC требует Plus** (прямо сказано в доке Ynison-плагина Music Assistant). ✅
- Без авторизации — только 30-секундные превью. ✅
- **HLS/MQA:** отдельного подтверждённого первичного описания HLS-стриминга через API я не нашёл. Основной путь у всех загрузчиков — прямые файлы (mp3/aac/flac), не HLS. HLS/MQA — **не подтверждено**. ⚠️

Источники: <https://github.com/llistochek/yandex-music-downloader>, <https://github.com/Stmol/yandex-music-downloader>, <https://www.music-assistant.io/plugins/yandex-ynison/>, <https://ym.marshal.dev/en/latest/yandex_music.download_info.html>.

---

## 4. Существующие open-source плееры — учиться / форкать

| Проект | Стек | Свежесть | Статус | Auth | Стриминг |
|---|---|---|---|---|---|
| [cucumber-sp/yandex-music-linux](https://github.com/cucumber-sp/yandex-music-linux) | Electron (репак Windows-клиента) | 2025-09 | ⛔ **archived** (Яндекс выпустил нативный Linux-клиент) | web-логин внутри Electron | как офиц. клиент |
| [TheKing-OfTime/YandexMusicModClient](https://github.com/TheKing-OfTime/YandexMusicModClient) | Electron mod (Node/JS патчи офиц. клиента) | 2026-02 | ⛔ **archived** (слияние с PulseSync) | как офиц. клиент | как офиц. клиент; **не** обходит Plus |
| PulseSync (преемник ↑) | Electron mod | активный ⚠️ | ✅ | как офиц. | как офиц. |
| [trudenboy/ma-provider-yandex-music](https://github.com/trudenboy/ma-provider-yandex-music) | Python (плагин Music Assistant) | 2026-08-11 | ✅ живой | OAuth-токен | **FLAC lossless, Моя волна, lyrics, рекомендации** |
| [trudenboy/ma-provider-yandex-ynison](https://github.com/trudenboy/ma-provider-yandex-ynison) | Python (Music Assistant, Ynison) | 2026-08-18 | ✅ живой | OAuth + Ynison WS | роль «пульта» для офиц. приложения |
| [DECE2183/yamusic-tui](https://github.com/DECE2183/yamusic-tui) | Go (TUI, поверх yandex-music-open-api) | 2026-03-26 | ✅ живой, 187★ | OAuth-токен | download-info |
| [Fidden/yandex-music-tauri](https://github.com/Fidden/yandex-music-tauri) | Tauri + Vue3/Nuxt/Pinia | 2023-10 | ⚠️ заброшен, 0★ | — | учебный проект |
| [rkashapov/mopidy-ymusic](https://github.com/rkashapov/mopidy-ymusic) | Python (Mopidy-расширение) | ⚠️ не проверял push | ⚠️ | токен | Mopidy backend |
| Mopidy-Yandexmusic (PyPI) | Python (Mopidy) | — | ⛔ inactive (Snyk) | — | — |
| [AlexxIT/YandexStation](https://github.com/AlexxIT/YandexStation) | Python (Home Assistant) | 2026-07-17 | ✅ очень живой, 1911★ | QR/cookies/token/password | управление Станцией + relay на Chromecast/DLNA/Sonos; **не** самостоятельный YM-плеер |
| [Judd1zzz/yandex-music-streamdeck](https://github.com/Judd1zzz/yandex-music-streamdeck) | Python/Rust (Stream Deck) | ✅ | ✅ | OAuth | локальное управление через **Chrome DevTools Protocol** (:9222) + частичный Ynison |

Заметки по ключевым:
- **YandexMusicModClient** добавлял: Discord RPC, удалённое управление, Last.FM scrobbling (требует ≥50% прослушивания), мини-плеер, кастомный кэш, глобальные хоткеи, показ codec/bitrate, скачивание треков/альбомов/плейлистов, DevTools/experiments. **Не обходит Plus.** Windows/macOS/Linux. Заархивирован 2026-02-09 → PulseSync. ✅
- **yandex-music-linux** — репак Windows-.exe под Electron; заархивирован после выхода нативного Linux-клиента Яндекса (2025-09-25). Урок: репак больше не нужен — есть официальный Linux-билд. ✅
- **RN/Flutter-клиентов** с открытым кодом в первичных источниках я **не нашёл** — по существу их нет / они незаметны. ⚠️
- foobar2000-компонент под Яндекс — **не найдено первичного источника.** ⚠️

---

## 5. Правовые / технические ограничения

**Правовые:** ⚠️ (документы найдены, детали по третьим приложениям построчно не цитирую)
- Действуют «Условия использования сервиса Яндекс Музыка» (<https://yandex.ru/legal/music_termsofuse/>) и «Лицензионное соглашение на использование программы Яндекс Музыка» (<https://yandex.ru/legal/music_mobile_agreement/>), как дополнение к общему Пользовательскому соглашению Яндекса.
- **Официального публичного API нет** — все библиотеки неофициальны и прямо это декларируют (MarshalX: «неофициальная библиотека»). ✅
- **Документированных случаев банов аккаунтов** именно за сторонний клиент/токен в первичных источниках я **не нашёл**. ⚠️ Риск существует по смыслу ToS, но эмпирических подтверждений в исследованных источниках нет.
- **Региональная доступность:** Яндекс Музыка гео-ограничена; Plus и каталог зависят от региона. (Общеизвестно; отдельный первичный пункт не цитирую.) ⚠️

**Технические:**
- **Нестабильность API** — схема подписи менялась (MD5 `get-mp3` → HMAC `get-file-info`), cookie-авторизация убрана в загрузчиках. ✅
- **Нет CORS** — из браузера напрямую нельзя, нужен прокси/бэкенд (существование `yandex-music-cors-proxy`). ✅
- **SmartCaptcha** — всплывает при программном логине по паролю; обходится через OAuth implicit/device flow / QR. ✅ (косвенно: «password unstable» в YandexStation)
- **device_id / Ynison-Device-Id** — требуется для Ynison (случайная строка ~16 симв.) и мобильных эндпоинтов. ⚠️
- **User-Agent** — для download-схемы важен; в v3 `llistochek` кастомный UA перестал влиять на авторизацию (убран `--user-agent`). ⚠️ Заголовок UA всё равно стоит слать «как у клиента».
- **Тайминги:** direct-link живёт ~1 мин (410 после). ✅

---

## 6. Рекомендуемые варианты архитектуры (синтез)

### Вариант A — тонкий клиент поверх неофициального REST (Tauri + Rust/TS, или web+прокси)
- **Что нужно:** ввод OAuth-токена → `account/status` → поиск → плейлисты → `download-info`/`get-file-info` → воспроизведение через `<audio>` (HTML5) по прямой ссылке → фидбек `play-audio`, чтобы рекомендации/«Моя волна» продолжали учиться.
- **Плюсы:** свой UI, лёгкий, кроссплатформа (Tauri ≈ 3–10 МБ vs Electron).
- **Минусы:** **из чистого браузера нельзя** (нет CORS) — нужен Rust-backend Tauri или локальный прокси; сами реализуете подпись `get-file-info` (HMAC) для FLAC; FLAC может требовать расшифровки. ⚠️
- **Auth/стрим:** токен вводит пользователь; стрим — прямые файлы, Plus обязателен.

### Вариант B — форк/мод существующего клиента
- **PulseSync** (преемник `YandexMusicModClient`) — если нужен готовый десктоп с плюшками (Discord RPC, scrobbling, download, hotkeys) поверх официального Electron-клиента. Минимум своей работы по API, но вы завязаны на официальный клиент и его обновления; Plus не обходится. ✅
- Не берите `yandex-music-linux` (архив) — используйте официальный нативный Linux-клиент Яндекса напрямую. ✅

### Вариант C — локальный демон/прокси (Python-библиотека) + любой фронтенд / MPD ⭐ рекомендуемый гибрид
- **Ядро:** Python-сервис на `MarshalX/yandex-music-api` — держит токен, делает поиск/плейлисты/`download-info`/rotor/`play-audio`, отдаёт REST/локальный HTTP наружу (заодно решает CORS).
- **Фронтенд:** любой — web SPA, Tauri, TUI, или MPD-совместимость через `mopidy-ymusic`/провайдер Music Assistant (`trudenboy/ma-provider-yandex-music`) если хотите headless + существующие MPD-клиенты.
- **Плюсы:** максимально живая кодовая база под капотом; вся грязная работа с подписями/авторизацией уже в библиотеке; фронтенд свободен.
- **Минусы:** нужен постоянно работающий локальный процесс.

> Если нужна синхронизация с официальным приложением («пульт») — добавьте Ynison-слой (`trudenboy/ma-provider-yandex-ynison` как референс). Для самостоятельного плеера — не требуется.

---

## 7. Минимальный набор эндпоинтов для плеера

> Логические функции (имена методов — как в MarshalX; точные URL сверяйте со Swagger `acherkashin/yandex-music-open-api` и исходником MarshalX, т.к. официального описания нет). ⚠️ на конкретных путях.

| Функция | Назначение | Метод (MarshalX) / путь ⚠️ | Достоверность |
|---|---|---|---|
| Аккаунт/статус | Проверка Plus, региона, валидности токена | `account_status()` → `/account/status` | ✅ метод / ⚠️ путь |
| Поиск | Треки/альбомы/артисты/плейлисты | `search(text, type)` → `/search` | ✅/⚠️ |
| Трек(и) | Метаданные по ID | `tracks([id])` → `/tracks` | ✅/⚠️ |
| Плейлисты пользователя | Список плейлистов, «Мне нравится» | `users_playlists_list()`, `users_likes_tracks()` | ✅/⚠️ |
| Плейлист | Содержимое | `users_playlists(kind)` → `/users/{uid}/playlists/{kind}` | ✅/⚠️ |
| **Download-info** | Ссылки на файл (MP3-схема) | `get_download_info()` / `.../tracks/{id}/download-info` → XML → `get_direct_link()` | ✅ |
| **get-file-info** | Высокое качество + FLAC (HMAC-подпись) | `/get-file-info` (HMAC-SHA256, ключ `7tvSmFbyf5hJnIHhCimDDD`) | ⚠️ |
| Ротор / «Моя волна» | Стрим-станция, next-треки | `rotor_station_tracks('user:onyourwave')`, `rotor_station_feedback()` | ✅ метод / ⚠️ детали |
| **Фидбек проигрывания** | Чтобы рекомендации/«Моя волна» учились | `play_audio(...)` → `/play-audio` (start/skip/end события) | ✅ метод / ⚠️ путь |
| Лайк/дизлайк (опц.) | Влияет на рекомендации | `users_likes_tracks_add/remove()` → `/users/{uid}/likes/tracks/{add-multiple,remove}` (снятие — именно `remove`, без «-multiple»: такого пути нет, отвечает 404) | ✅ живой прогон 2026-08 |
| Ynison (опц.) | Синхронизация/«пульт» | `wss://ynison.music.yandex.net` (+redirector), заголовки `Ynison-Device-Id`, `Ynison-Device-Info`, Bearer | ⚠️ |

**Минимальный happy-path воспроизведения:**
1. `account_status` (убедиться, что Plus активен).
2. `search` / `users_playlists` / `rotor_station_tracks('user:onyourwave')` — получить очередь треков.
3. Для трека: `get_download_info` → выбрать codec/bitrate → `get_direct_link` (или `get-file-info` для FLAC). Ссылка живёт ~1 мин.
4. Воспроизвести (`<audio>` / нативный декодер).
5. Слать `play_audio`-события (start/end/skip) — иначе «Моя волна» и рекомендации деградируют.

---

## Приложение: сводка достоверности

- **Проверено по исходнику:** MD5-схема `get-mp3` и соль `XGRlBW9FXlekgbPrRHuSiA` (`download_info.py`); поля `DownloadInfo`; 1-минутный TTL; статус/лицензии репозиториев (GitHub API); архивация `yandex-music-linux` и `YandexMusicModClient`; отсутствие CORS (через существование прокси); Plus обязателен для FLAC (Ynison-плагин MA); implicit-OAuth client_id; невозможность своего OAuth-app.
- **НЕ проверено построчно (⚠️):** HMAC-ключ `7tvSmFbyf5hJnIHhCimDDD` и точный формат подписи `get-file-info` (из README/поиска, не из исходника); детали Ynison-хендшейка (заголовки/redirector); точные REST-пути эндпоинтов; отсутствие/наличие банов; HLS/MQA; RN/Flutter/foobar2000-клиенты (не найдены).
- **Перед реализацией стриминга FLAC** обязательно сверьте подпись `get-file-info` с актуальным исходником `llistochek/yandex-music-downloader` и/или `Stmol/yandex-music-downloader`.
