# ym-go-player

Неофициальный быстрый плеер и клиент для Яндекс Музыки на Go и TypeScript.

[![Release](https://img.shields.io/github/v/release/neolite/ym-go-player)](https://github.com/neolite/ym-go-player/releases)
[![Build Status](https://github.com/neolite/ym-go-player/workflows/Release/badge.svg)](https://github.com/neolite/ym-go-player/actions)
[![Go Version](https://img.shields.io/github/go-mod/go-version/neolite/ym-go-player)](go.mod)

---

## 🚀 Варианты использования

Проект поставляется в виде двух исполняемых файлов:

1. **`musicapp`** — Нативное десктопное приложение в отдельном окне (без необходимости открывать браузер). Построено с использованием [Wails v2](https://wails.io/).
2. **`musicd`** — Фоновый демон, запускающий локальный HTTP-сервер (по умолчанию на `127.0.0.1`) с веб-интерфейсом плеера и автоматически открывающий его в браузере по умолчанию.

---

## ✨ Особенности

- **Авторизация**: Вход по QR-коду / коду устройства через Yandex Device Auth Flow.
- **Безопасность**: Хранение авторизационного токена в системном хранилище ключей (OS Keyring: macOS Keychain, Windows Credential Manager, Linux Secret Service / KWallet).
- **Моя Волна**: Поддержка радиопотока Яндекс Музыки (Rotor/Wave) с обратной связью (лайк, дизлайк, пропуск).
- **Медиатека и поиск**: Просмотр альбомов, карточек артистов, любимых треков и глобальный поиск.
- **Легковесность**: Веб-интерфейс скомпилирован на чистом TypeScript с помощью esbuild и вшит прямо в бинарные файлы (через `go:embed`).

---

## 📦 Готовые релизы

Готовые бинарные файлы для всех ОС доступны в разделе **[Releases](https://github.com/neolite/ym-go-player/releases)**:

- **Linux (amd64)**: `ym-go-player-linux-amd64.tar.gz`
- **macOS (arm64)**: `ym-go-player-darwin-arm64.tar.gz`
- **Windows (amd64)**: `ym-go-player-windows-amd64.zip`

---

## 🛠️ Сборка из исходников

### Требования

- **Go**: 1.23 или новее
- **Node.js**: 20+ и `npm`
- **Linux** (дополнительно для сборки Wails): `libgtk-3-dev`, `libwebkit2gtk-4.1-dev` (или `4.0`), `libgl1-mesa-dev`, `pkg-config`

### Команды сборки

1. **Сборка веб-фронтенда**:
   ```bash
   make web
   ```

2. **Сборка фонового демона (`musicd`)**:
   ```bash
   make build
   ./musicd
   ```

3. **Сборка нативного приложения (`musicapp`)**:
   - **macOS**:
     ```bash
     CGO_LDFLAGS="-framework UniformTypeIdentifiers" go build -tags "desktop,production" -o musicapp ./cmd/musicapp
     ```
   - **Linux**:
     ```bash
     go build -tags "desktop,production,webkit2_41" -o musicapp ./cmd/musicapp
     ```
   - **Windows**:
     ```bash
     go build -tags "desktop,production" -o musicapp.exe ./cmd/musicapp
     ```

---

## ⚙️ Флаги запуска `musicd`

```text
  -addr string
    	Адрес интерфейса (по умолчанию "127.0.0.1:0" — случайный свободный порт)
  -no-keychain
    	Хранить токен авторизации только в оперативной памяти процесса (без OS Keyring)
  -no-open
    	Не открывать браузер автоматически при старте
```

---

## 📜 Лицензия

MIT
