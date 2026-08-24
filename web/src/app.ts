// Фронтенд владеет воспроизведением; демон владеет очередью и фидбеком.
// Состояние приходит по SSE и здесь не дублируется: страница только рисует.
// Единственное, что живёт на клиенте, — то, чего нет в состоянии: флаг
// перетаскивания ползунка, счётчик неудачных попыток воспроизведения,
// пауза переподключения.

import { BAND_FREQS, initEqualizer, getGains, setBandGain, resetGains } from "./equalizer";

type Status = "idle" | "loading" | "playing" | "paused" | "error";

interface Track {
  id: string; title: string; artists: string[]; artistIds: string[];
  album: string; albumId: string; coverUrl: string; duration: number; available: boolean; liked: boolean;
}

interface State {
  status: Status; track: Track | null; position: number; duration: number;
  volume: number; queue: Track[]; queueIndex: number; source: string; error?: string;
}

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const audio = $<HTMLAudioElement>("audio");
const listEl = $<HTMLUListElement>("list");
const listsEl = $<HTMLUListElement>("lists");
const errorBar = $<HTMLDivElement>("errorBar");
const bgArtEl = $<HTMLDivElement>("bgArt");
let currentTrackId = "";
let lastLiked = false;

// Позицию внутри трека помнит сервер (State.position), но <audio> после
// перезагрузки страницы всегда стартует с нуля. Восстанавливаем её один
// раз на loadedmetadata — раньше currentTime недоступен. Обычную смену
// трека это не задевает: сервер сбрасывает позицию при переходе
// (resetPosition в routes.go), и pendingSeek остаётся нулём.
let pendingSeek = 0;
audio.addEventListener("loadedmetadata", () => {
  if (pendingSeek > 0) {
    audio.currentTime = pendingSeek;
    pendingSeek = 0;
  }
});

// --- ошибки ---
//
// Одна строка разметки (#errorBar), два разных источника с разной жизнью:
//
// - showTransientError — ОШИБКА-СОБЫТИЕ (неуспешный разовый api()-вызов).
//   Пользователь увидел, за него всё равно ответит следующее действие —
//   поэтому автоскрытие через несколько секунд уместно.
// - setStateError / setFailureError — ОШИБКА-СОСТОЯНИЕ (State.error из SSE
//   и сообщение о пределе неудач воспроизведения соответственно). Это не
//   событие, а описание текущего положения дел, которое остаётся верным,
//   пока его явно не снимут. Таймер автоскрытия здесь неуместен при любом
//   режиме потока кадров: во время воспроизведения handleProgress публикует
//   кадр каждые 5 с, и каждый несёт тот же error — таймер не скрывал бы
//   ошибку, а заставлял бы её мигать; вне воспроизведения кадры идут по
//   изменению состояния, и таймер вернул бы пользователя в то самое
//   молчаливо сломанное состояние, ради избежания которого её вообще
//   показывали. Отдельные слоты для устойчивых источников — потому что у
//   них разные условия снятия (новый кадр без error / событие "playing" /
//   следующий checkAuth), и один частый источник (SSE-кадр по другому
//   поводу) не должен затирать сообщение другого.
//
// Разовая ошибка может временно перекрыть устойчивую — после её
// автоскрытия строка возвращается к устойчивой, если та ещё в силе.

let stickyStateError = "";
let stickyFailureError = "";
// Третий устойчивый источник — предупреждение из /api/auth/status (сейчас
// это неактивный Плюс). Условие снятия у него своё — следующий checkAuth,
// — поэтому отдельный слот, а не чужой.
let stickyAuthWarning = "";
let transientHideTimer: number | undefined;

function currentSticky(): string {
  // Сообщение о пределе неудач актуальнее: оно объясняет, почему плеер
  // молчит прямо сейчас, даже если параллельно пришёл кадр SSE без error.
  return stickyFailureError || stickyStateError || stickyAuthWarning;
}

function paintErrorBar(): void {
  if (transientHideTimer !== undefined) return; // разовая ошибка ещё на экране — не мешаем её таймеру
  const msg = currentSticky();
  if (msg) {
    errorBar.textContent = msg;
    errorBar.classList.remove("hidden");
  } else {
    errorBar.classList.add("hidden");
  }
}

function showTransientError(message: string): void {
  if (!message) return;
  window.clearTimeout(transientHideTimer);
  errorBar.textContent = message;
  errorBar.classList.remove("hidden");
  transientHideTimer = window.setTimeout(() => {
    transientHideTimer = undefined;
    paintErrorBar();
  }, 4000);
}

function setStateError(message: string): void {
  stickyStateError = message;
  paintErrorBar();
}

function setFailureError(message: string): void {
  stickyFailureError = message;
  paintErrorBar();
}

function setAuthWarning(message: string): void {
  stickyAuthWarning = message;
  paintErrorBar();
}

// Все маршруты, которые вызывает api(), — POST (routes.go:48-58). Из
// GET-роутов фронтенд вызывает только /api/playlists — отдельным fetch
// мимо этой функции; /api/likes и /api/search из фронта не вызываются
// вовсе (поиск идёт через POST /api/play с source:"search"). method обязан
// быть POST всегда, а не только когда есть тело:
// fetch(path, {}) без явного method уходит GET, и без тела (next/prev/
// pause/resume) все эти вызовы получали 404.
// Текст ошибки из ответа сервера. apiError в routes.go шлёт JSON вида
// {"error": "..."} — показываем само сообщение, а не сырой JSON; ответы
// не-JSON (http.Error из OriginGuard) уходят в строку как есть.
async function errorText(res: Response): Promise<string> {
  const text = await res.text();
  try {
    const parsed = JSON.parse(text);
    if (parsed && typeof parsed.error === "string") return parsed.error;
  } catch { /* не JSON — оставляем исходный текст */ }
  return text;
}

async function api(path: string, body?: unknown, method = "POST"): Promise<any> {
  const res = await fetch(path, body === undefined
    ? { method }
    : {
      method,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  if (!res.ok) {
    const message = await errorText(res);
    showTransientError(message);
    throw new Error(message);
  }
  return res.status === 204 ? null : res.json();
}

// --- тема ---
//
// Пять тем, выбираемых переключалкой #themeSwitch: базовая SF, кислотная,
// лаборатория (иммерсивная), blok (пост-советский брутализм), neon
// (глубокий синий, люминесцентные глифы). Это чисто визуальные
// слои (класс на <body>), логику плеера не трогают. Выбор хранится в
// localStorage и применяется до первого кадра SSE, чтобы интерфейс не
// мигал другой темой при загрузке.

const THEME_KEY = "music212.theme";
let currentTheme = "";

function applyTheme(theme: string, persist = true): void {
  currentTheme = theme;
  document.body.classList.toggle("acid", theme === "acid");
  document.body.classList.toggle("lab", theme === "lab");
  document.body.classList.toggle("blok", theme === "blok");
  document.body.classList.toggle("neon", theme === "neon");
  document.querySelectorAll<HTMLButtonElement>("#themeSwitch button").forEach((b) => {
    b.classList.toggle("active", (b.dataset.theme ?? "") === theme);
  });
  if (!persist) return;
  try { localStorage.setItem(THEME_KEY, theme); } catch { /* приватный режим — тема просто не запомнится */ }
}

// Якорь вида #theme-lab / #theme-acid / #theme-blok / #theme-neon включает
// тему разово,
// не трогая сохранённый выбор, — удобно для ссылок-демо и headless-проверок.
const hashTheme = location.hash.match(/^#theme-(acid|lab|blok|neon)$/);
try {
  applyTheme(hashTheme ? hashTheme[1] : (localStorage.getItem(THEME_KEY) ?? ""), !hashTheme);
} catch { /* нет localStorage — остаётся базовая тема */ }

document.querySelectorAll<HTMLButtonElement>("#themeSwitch button").forEach((btn) => {
  btn.addEventListener("click", () => { applyTheme(btn.dataset.theme ?? ""); });
});

// --- авторизация ---

// После входа Яндекс кладёт токен в адресную строку вида
// https://music.yandex.ru/#access_token=...&token_type=bearer&expires_in=...&cid=...
// Естественное действие пользователя — скопировать её целиком (или только
// хвост от access_token и дальше, если # не попал в буфер). Разбираем
// текст, а не полагаемся на его форму: ищем "access_token" где угодно в
// строке — во фрагменте, в хвосте без "#", неважно. Если разметки нет
// вовсе, считаем, что ввели голый токен, и не трогаем его. Никогда не
// бросает исключение и ничего не логирует — на мусоре просто возвращает
// исходный текст, пусть сервер ответит своим сообщением.
function extractToken(raw: string): string {
  const text = raw.trim();
  const marker = "access_token";
  const idx = text.indexOf(marker);
  if (idx === -1) return text;

  const afterMarker = text.slice(idx + marker.length);
  const eq = afterMarker.indexOf("=");
  if (eq === -1) return text; // "access_token" нашёлся, но не как параметр — не гадаем

  let value = afterMarker.slice(eq + 1);
  const amp = value.indexOf("&");
  if (amp !== -1) value = value.slice(0, amp);
  if (!value) return text; // access_token= без значения — разобрать не вышло

  try {
    return decodeURIComponent(value);
  } catch {
    return value; // некорректные %-последовательности — не падаем, берём как есть
  }
}

async function checkAuth(): Promise<boolean> {
  const st = await (await fetch("/api/auth/status")).json();
  if (st.authorized) {
    stopDevicePoll();
    $("auth").classList.add("hidden");
    $("app").classList.remove("hidden");
    // Сервер сообщает о неактивном Плюсе в st.message (stateFrom в
    // auth.go) — раньше при authorized это сообщение просто выбрасывалось,
    // и пользователь без подписки узнавал о ней только по сбоям
    // воспроизведения.
    setAuthWarning(st.message ?? "");
    return true;
  }
  setAuthWarning("");
  $("auth").classList.remove("hidden");
  $("app").classList.add("hidden");
  $("authMsg").textContent = st.message ?? "";
  ($("authLink") as HTMLAnchorElement).href = st.authUrl ?? "#";
  return false;
}

let devicePollTimer: number | undefined;

function stopDevicePoll(): void {
  if (devicePollTimer !== undefined) {
    window.clearInterval(devicePollTimer);
    devicePollTimer = undefined;
  }
}

$("btnGetDeviceCode")?.addEventListener("click", async () => {
  stopDevicePoll();
  $("authMsg").textContent = "";
  $("deviceStatusMsg").textContent = "Запрос кода...";
  $("deviceCodeBox").classList.remove("hidden");

  try {
    const res = await fetch("/api/auth/device/code", { method: "POST" });
    if (!res.ok) {
      const err = await errorText(res);
      $("authMsg").textContent = err || "Ошибка получения кода";
      $("deviceCodeBox").classList.add("hidden");
      return;
    }
    const data = await res.json();
    $("deviceUserCode").textContent = data.user_code || "—";
    const authUrl = data.verification_url || "https://oauth.yandex.ru/activate";
    ($("deviceAuthUrl") as HTMLAnchorElement).href = authUrl;
    $("deviceStatusMsg").textContent = "Ожидание подтверждения кода на странице Яндекса...";

    const pollInterval = Math.max(3, data.interval || 5) * 1000;
    const deviceCode = data.device_code;

    devicePollTimer = window.setInterval(async () => {
      try {
        const pollRes = await fetch("/api/auth/device/poll", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ deviceCode }),
        });
        const pollData = await pollRes.json();

        if (pollRes.ok && pollData.authorized) {
          stopDevicePoll();
          $("deviceStatusMsg").textContent = "Успешно!";
          await checkAuth();
          connect();
        } else if (pollRes.status === 202 || pollData.pending) {
          // Всё ещё ожидаем действия пользователя
        } else {
          stopDevicePoll();
          $("authMsg").textContent = pollData.message || "Ошибка авторизации по коду";
          $("deviceStatusMsg").textContent = "Авторизация отменена или истёк срок действия кода";
        }
      } catch {
        // Сетевой сбой — пропускаем итерацию поллинга
      }
    }, pollInterval);
  } catch (e: any) {
    $("authMsg").textContent = e.message || "Сетевая ошибка";
    $("deviceCodeBox").classList.add("hidden");
  }
});

$("tokenSave").addEventListener("click", async () => {
  stopDevicePoll();
  const token = extractToken(($("tokenInput") as HTMLInputElement).value);
  const res = await fetch("/api/auth/token", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ token }),
  });
  const st = await res.json();
  $("authMsg").textContent = st.message ?? "";
  if (res.ok) { await checkAuth(); connect(); }
});

// --- восстановление контекста ---
//
// Демон держит очередь только в памяти процесса: после его перезапуска
// страница получает пустое состояние и остаётся без трека и обложки.
// Поэтому фронтенд запоминает контекст (источник, трек, позицию) в
// localStorage и, если первый кадр SSE пришёл пустым, просит демон собрать
// тот же источник заново. Восстанавливаемся только в «волну» и «Мне
// нравится»: параметры запроса для playlist/search (kind, query) в State
// не сохраняются, и по одному source их не восстановить.

const RESUME_KEY = "music212.resume";

interface ResumeContext { source: string; trackId: string; position: number; }

// resumeContext живёт между первым кадром (где решено восстановиться) и
// первой сменой трека (где позиция либо применилась, либо сгорела).
let resumeContext: ResumeContext | null = null;

function loadResumeContext(): ResumeContext | null {
  try {
    const raw = localStorage.getItem(RESUME_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw);
    if (typeof parsed?.source !== "string") return null;
    if (parsed.source !== "wave" && parsed.source !== "likes") return null;
    return parsed as ResumeContext;
  } catch {
    return null; // битая запись — не повод ронять загрузку
  }
}

function saveResumeContext(): void {
  if (!lastState?.track) return;
  try {
    localStorage.setItem(RESUME_KEY, JSON.stringify({
      source: lastState.source,
      trackId: lastState.track.id,
      position: audio.currentTime,
    } as ResumeContext));
  } catch { /* приватный режим или квота — сохранение просто не сработает */ }
}

// onFirstFrame решает, нужно ли восстанавливаться: только если сам демон
// ничего не помнит (трека в кадре нет). Кадр с треком означает, что
// очередь жива, и локальная память не нужна. Сохранённого контекста может
// и не быть (первый запуск вообще) — тогда стартуем «волну» по умолчанию:
// пустое состояние бывает только у свежего демона, и молчаливое пустое
// окно хуже, чем самостоятельно заигравшее радио.
let firstFrameSeen = false;

function onFirstFrame(st: State): void {
  if (st.track) return;
  const saved = loadResumeContext();
  resumeContext = saved;
  api("/api/play", { source: saved?.source ?? "wave" }).catch(() => {});
}

// entityLink — кликабельное имя артиста/альбома. stopPropagation обязателен
// для строк очереди: там весь <li> уже несёт свой click (переход по
// индексу очереди), и клик по имени внутри не должен запускать оба.
function entityLink(kind: "artists" | "albums", id: string, text: string): HTMLSpanElement {
  const span = document.createElement("span");
  span.className = "entityLink";
  span.textContent = text;
  span.addEventListener("click", (e) => {
    e.stopPropagation();
    openEntityCard(kind, id, text).catch(() => {});
  });
  return span;
}

// --- отрисовка ---

function render(st: State): void {
  lastState = st;
  const t = st.track;
  // Оборона клиента: сервер обязан слать [] вместо null для срезов
  // состояния (см. internal/player/queue.go), но один разорванный кадр не
  // должен обрывать отрисовку на середине — отсюда `?? []` на каждом
  // массиве состояния, а не только на верхнем st.queue.
  const queue = st.queue ?? [];
  $("title").textContent = t ? t.title : "—";
  const artistWrap = $("artist");
  artistWrap.innerHTML = "";
  (t?.artists ?? []).forEach((name, i) => {
    if (i > 0) artistWrap.append(", ");
    const id = t?.artistIds?.[i];
    artistWrap.append(id ? entityLink("artists", id, name) : document.createTextNode(name));
  });
  const albumWrap = $("album");
  albumWrap.innerHTML = "";
  if (t?.album) {
    albumWrap.append(t.albumId ? entityLink("albums", t.albumId, t.album) : document.createTextNode(t.album));
  }
  // Пустому src браузер рисует иконку битой картинки — в пустом состоянии
  // прячем обложку целиком.
  const coverEl = $("cover") as HTMLImageElement;
  if (t?.coverUrl) {
    coverEl.src = t.coverUrl;
    coverEl.classList.remove("hidden");
  } else {
    coverEl.removeAttribute("src");
    coverEl.classList.add("hidden");
  }
  // Иконка в кнопке play — два SVG, видимость переключает класс playing.
  $<HTMLButtonElement>("btnPlay").classList.toggle("playing", st.status === "playing");
  // Активный источник подсвечиваем в шапке — иначе по интерфейсу не видно,
  // что сейчас играет: «волна» или «нравится».
  $("btnWave").classList.toggle("active", st.source === "wave");
  $("btnLikes").classList.toggle("active", st.source === "likes");

  // Статус лайка текущего трека: класс liked переключаем всегда (дешёво и
  // идемпотентно), а анимацию pop — только на фактической смене значения,
  // иначе она переигрывалась бы на каждом кадре SSE (несколько раз в
  // секунду во время воспроизведения).
  const liked = t?.liked ?? false;
  const btnLike = $<HTMLButtonElement>("btnLike");
  btnLike.classList.toggle("liked", liked);
  if (liked !== lastLiked) {
    btnLike.classList.remove("pop");
    void btnLike.offsetWidth; // форсируем reflow, чтобы повторный класс снова запустил анимацию
    btnLike.classList.add("pop");
  }
  lastLiked = liked;

  // st.error показываем независимо от того, есть трек или нет — ошибка
  // источника (например, отсутствие Плюса) не обязана ждать пустого трека.
  // Устойчивая ошибка, а не разовая: держится, пока кадр её несёт, и
  // снимается ровно тем кадром, где error снова пуст.
  setStateError(st.error ?? "");

  // Громкость между перезагрузками страницы помнит только сервер — после
  // перезагрузки <audio> и ползунок всегда стартуют с 1.0. Применяем её из
  // кадра, но не во время жеста (volumeDragging). Нуль трактуем как «не
  // задано»: у демона нет отдельного признака, что громкость когда-либо
  // выставляли, и свежий процесс шлёт 0 — применение его глушило бы звук
  // на каждой загрузке. Цена допущения: намеренная тишина (volume = 0)
  // перезагрузку страницы не переживает.
  if (!volumeDragging && st.volume > 0) {
    audio.volume = st.volume;
    volumeEl.value = String(st.volume);
  }

  listEl.innerHTML = "";
  queue.forEach((track, i) => {
    const li = document.createElement("li");
    const idx = document.createElement("span");
    idx.className = "idx";
    idx.textContent = String(i + 1);
    // Миниатюра обложки — в базовых темах скрыта CSS'ом, в lab очередь
    // рисуется каруселью обложек.
    const qc = document.createElement("img");
    qc.className = "qcover";
    qc.loading = "lazy";
    qc.alt = "";
    if (track.coverUrl) qc.src = track.coverUrl;
    const name = document.createElement("span");
    name.className = "name";
    const titleEl = document.createElement("span");
    titleEl.className = "t";
    titleEl.textContent = track.title;
    const artistEl = document.createElement("span");
    artistEl.className = "a";
    artistEl.append(" — ");
    (track.artists ?? []).forEach((name, ai) => {
      if (ai > 0) artistEl.append(", ");
      const id = track.artistIds?.[ai];
      artistEl.append(id ? entityLink("artists", id, name) : document.createTextNode(name));
    });
    name.append(titleEl, artistEl);
    const dur = document.createElement("span");
    dur.className = "dur";
    dur.textContent = fmtTime(track.duration);
    li.append(idx, qc, name, dur);
    // Недоступные в регионе треки видны, но приглушены: сервер их пропустит
    // (skipUnavailable), список честно показывает, почему цифры прыгают.
    if (!track.available) {
      li.classList.add("unavailable");
      li.title = "Недоступен в вашем регионе";
    }
    li.classList.toggle("current", i === st.queueIndex);
    li.addEventListener("click", () => {
      // Клик по треку ТЕКУЩЕЙ очереди — это переход внутри неё
      // (handleQueueIndex): очередь и её source не меняются, поэтому для
      // волны продолжают работать подкачка батчей (refillWave) и фидбек
      // ротора — оба гейтятся на source == "wave". Ставить здесь новую
      // очередь через /api/play {"source":"tracks"} нельзя: после этого оба
      // механизма отключились бы навсегда. /api/play со свежим списком —
      // только для новых источников (плейлисты, «Мне нравится», поиск).
      api("/api/player/queue-index", { index: i }).catch(() => {});
    });
    listEl.appendChild(li);
  });

  // Смена трека — единственный повод трогать src: иначе перезапустим текущий.
  if (t && t.id !== currentTrackId) {
    currentTrackId = t.id;
    // Новый трек закрывает прошлый сетевой откат, если тот ещё тикал.
    netRetryActive = false;
    netRetryDelay = 1000;
    window.clearTimeout(netRetryTimer);
    // Позиция сервера в приоритете; из localStorage — только если демон
    // после перезапуска вернул тот же самый трек (для «волны» это обычно
    // не так, и прыжок в середину чужого трека хуже старта с нуля).
    pendingSeek = st.position > 0 ? st.position
      : (resumeContext && resumeContext.trackId === t.id ? resumeContext.position : 0);
    resumeContext = null;
    // Ползунок двигает только timeupdate, и до первого события нового трека
    // он продолжал бы показывать позицию предыдущего (а при запрещённом
    // автоплее — навсегда). Ставим сразу по данным кадра: duration трека
    // в состоянии есть всегда, ждать метаданные <audio> не нужно.
    setProgress(t.duration > 0 ? (pendingSeek / t.duration) * 100 : 0);
    $("timeCur").textContent = fmtTime(pendingSeek);
    $("timeTotal").textContent = fmtTime(t.duration);
    $("bigTime").textContent = fmtTime(pendingSeek);
    // Живой фон темы lab — размытая обложка текущего трека.
    bgArtEl.style.backgroundImage = t.coverUrl ? `url("${t.coverUrl}")` : "";
    audio.src = `/stream/${t.id}`;
    audio.play().catch(() => {});
    updateMediaSession(t);
  }
  if (!t) {
    currentTrackId = "";
    audio.removeAttribute("src");
    setProgress(0);
    $("timeCur").textContent = fmtTime(0);
    $("timeTotal").textContent = fmtTime(0);
    $("bigTime").textContent = fmtTime(0);
    bgArtEl.style.backgroundImage = "";
  }
}

// MediaSession даёт медиа-клавиши и карточку «сейчас играет» в системе —
// то, чего обычная вкладка иначе не умеет.
function updateMediaSession(t: Track): void {
  if (!("mediaSession" in navigator)) return;
  navigator.mediaSession.metadata = new MediaMetadata({
    title: t.title,
    artist: (t.artists ?? []).join(", "),
    album: t.album,
    artwork: t.coverUrl ? [{ src: t.coverUrl, sizes: "400x400", type: "image/jpeg" }] : [],
  });
  // play() может штатно отклониться (политика браузера, src ещё не готов) —
  // молчим: то же самое отражает следующий кадр SSE, и рассинхрона не будет.
  navigator.mediaSession.setActionHandler("play", () => { audio.play().catch(() => {}); api("/api/player/resume").catch(() => {}); });
  navigator.mediaSession.setActionHandler("pause", () => { audio.pause(); api("/api/player/pause").catch(() => {}); });
  navigator.mediaSession.setActionHandler("nexttrack", () => { api("/api/player/next").catch(() => {}); });
  navigator.mediaSession.setActionHandler("previoustrack", () => { api("/api/player/prev").catch(() => {}); });
}

// --- поток состояния ---

let source: EventSource | null = null;
let reconnectDelay = 1000;
const maxReconnectDelay = 30000;

// Кадры /api/events безымянные (нет строки "event:", только "data:"), а
// значит слушать их обязан именно onmessage. Именованный
// es.addEventListener("state", ...) — естественная на вид, но неверная
// форма: он не получит ни одного кадра, интерфейс останется навсегда
// пустым, и ни ошибки, ни предупреждения в консоли при этом не будет.
// Вся подписка нарочно собрана в одну эту функцию, чтобы разбор кадра
// нигде больше в файле не завёлся по ошибке.
function subscribe(es: EventSource, onState: (st: State) => void): void {
  es.onmessage = (e) => onState(JSON.parse(e.data) as State);
}

function connect(): void {
  source?.close();
  source = new EventSource("/api/events");
  subscribe(source, (st) => {
    if (!firstFrameSeen) { firstFrameSeen = true; onFirstFrame(st); }
    render(st);
  });
  source.onopen = () => { reconnectDelay = 1000; };
  source.onerror = () => {
    source?.close();
    setTimeout(connect, reconnectDelay);
    reconnectDelay = Math.min(reconnectDelay * 2, maxReconnectDelay);
  };
}

// --- управление ---

$("btnWave").addEventListener("click", () => { api("/api/play", { source: "wave" }).catch(() => {}); });
$("btnLikes").addEventListener("click", () => { api("/api/play", { source: "likes" }).catch(() => {}); });
$("btnNext").addEventListener("click", () => { api("/api/player/next").catch(() => {}); });
$("btnPrev").addEventListener("click", () => { api("/api/player/prev").catch(() => {}); });

$("btnPlay").addEventListener("click", () => {
  // play() может штатно отклониться — молчим, как и в render()/MediaSession.
  if (audio.paused) { audio.play().catch(() => {}); api("/api/player/resume").catch(() => {}); }
  else { audio.pause(); api("/api/player/pause").catch(() => {}); }
});

$("btnLists").addEventListener("click", async () => {
  // Плейлисты рисуются в отдельный #lists, а не в #list, где живёт очередь:
  // раньше они делили один <ul>, и первый же кадр SSE (render перерисовывает
  // #list целиком) затирал список плейлистов прямо под курсором. Повторный
  // клик по кнопке возвращает очередь.
  if (!listsEl.classList.contains("hidden")) { showQueue(); return; }
  const res = await fetch("/api/playlists");
  if (!res.ok) { showTransientError(await errorText(res)); return; }
  const lists = await res.json();
  $("cardHeader").classList.add("hidden");
  listsEl.innerHTML = "";
  for (const pl of lists) {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name";
    const titleEl = document.createElement("span");
    titleEl.className = "t";
    titleEl.textContent = pl.title;
    name.append(titleEl);
    const dur = document.createElement("span");
    dur.className = "dur";
    dur.textContent = String(pl.trackCount);
    li.append(name, dur);
    li.addEventListener("click", () => {
      api("/api/play", { source: "playlist", kind: pl.kind }).catch(() => {});
      showQueue();
    });
    listsEl.appendChild(li);
  }
  listEl.classList.add("hidden");
  listsEl.classList.remove("hidden");
});

function showQueue(): void {
  listsEl.classList.add("hidden");
  $("cardHeader").classList.add("hidden");
  listEl.classList.remove("hidden");
}

// openEntityCard — карточка артиста/альбома поверх той же панели #lists,
// что уже рисует список плейлистов. Открытие карточки не трогает
// воспроизведение: очередь меняется только явным кликом по треку внутри.
async function openEntityCard(kind: "artists" | "albums", id: string, title: string): Promise<void> {
  const res = await fetch(`/api/${kind}/${id}/tracks`);
  if (!res.ok) { showTransientError(await errorText(res)); return; }
  const tracks = await res.json() as Track[];
  listsEl.innerHTML = "";
  tracks.forEach((track, i) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.className = "name";
    const titleEl = document.createElement("span");
    titleEl.className = "t";
    titleEl.textContent = track.title;
    name.append(titleEl);
    const dur = document.createElement("span");
    dur.className = "dur";
    dur.textContent = fmtTime(track.duration);
    li.append(name, dur);
    li.addEventListener("click", () => {
      // Список от кликнутого трека и до конца — воспроизведение
      // продолжается дальше по карточке в том же порядке.
      api("/api/play", { source: "tracks", tracks: tracks.slice(i) }).catch(() => {});
      showQueue();
    });
    listsEl.appendChild(li);
  });
  $("cardTitle").textContent = title;
  $("cardHeader").classList.remove("hidden");
  listEl.classList.add("hidden");
  listsEl.classList.remove("hidden");
}

$("cardBack").addEventListener("click", showQueue);

$("search").addEventListener("keydown", (e) => {
  if ((e as KeyboardEvent).key !== "Enter") return;
  const q = ($("search") as HTMLInputElement).value.trim();
  if (q) api("/api/play", { source: "search", query: q }).catch(() => {});
});

// Ползунок громкости ведёт себя как ползунок прогресса: пока его держат,
// фоновые кадры SSE не должны выдёргивать его из-под курсора (render
// применяет State.volume — см. ниже). Программная установка .value события
// "input" не порождает, поэтому обратного POST и петли не возникает.
let volumeDragging = false;
const volumeEl = $<HTMLInputElement>("volume");

volumeEl.addEventListener("pointerdown", () => { volumeDragging = true; });
volumeEl.addEventListener("pointerup", () => { volumeDragging = false; });
volumeEl.addEventListener("change", () => { volumeDragging = false; });

volumeEl.addEventListener("input", () => {
  const v = parseFloat(volumeEl.value);
  audio.volume = v;
  api("/api/player/volume", { volume: v }).catch(() => {});
});

// --- эквалайзер ---
//
// Панель строится один раз, при первом открытии (buildEqPanel идемпотентна
// через eqBuilt) — граф Web Audio (initEqualizer) заводится там же, тоже
// лениво: см. web/src/equalizer.ts.

const eqPanel = $<HTMLDivElement>("eqPanel");
const btnEq = $<HTMLButtonElement>("btnEq");
let eqBuilt = false;

function fmtDb(db: number): string {
  return (db > 0 ? "+" : "") + db + " дБ";
}

function fmtFreq(freq: number): string {
  return freq >= 1000 ? `${freq / 1000} кГц` : `${freq} Гц`;
}

function buildEqPanel(): void {
  if (eqBuilt) return;
  eqBuilt = true;
  const gains = getGains();
  const bands = document.createElement("div");
  bands.className = "eqBands";
  BAND_FREQS.forEach((freq, i) => {
    const col = document.createElement("div");
    col.className = "eqBand";
    const val = document.createElement("span");
    val.className = "eqVal";
    val.textContent = fmtDb(gains[i]);
    const sliderWrap = document.createElement("div");
    sliderWrap.className = "eqSliderWrap";
    const slider = document.createElement("input");
    slider.type = "range";
    slider.className = "eqSlider";
    slider.min = "-12";
    slider.max = "12";
    slider.step = "1";
    slider.value = String(gains[i]);
    const label = document.createElement("span");
    label.className = "eqFreq";
    label.textContent = fmtFreq(freq);
    slider.setAttribute("aria-label", label.textContent);
    slider.addEventListener("input", () => {
      const db = parseFloat(slider.value);
      setBandGain(i, db);
      val.textContent = fmtDb(db);
    });
    sliderWrap.appendChild(slider);
    col.append(val, sliderWrap, label);
    bands.appendChild(col);
  });
  eqPanel.appendChild(bands);

  const reset = document.createElement("button");
  reset.id = "btnEqReset";
  reset.textContent = "Сброс";
  reset.addEventListener("click", () => {
    resetGains();
    eqPanel.querySelectorAll<HTMLInputElement>(".eqSlider").forEach((s) => { s.value = "0"; });
    eqPanel.querySelectorAll<HTMLSpanElement>(".eqVal").forEach((v) => { v.textContent = fmtDb(0); });
  });
  eqPanel.appendChild(reset);
}

btnEq.addEventListener("click", () => {
  const opening = eqPanel.classList.contains("hidden");
  if (opening) {
    initEqualizer(audio);
    buildEqPanel();
  }
  eqPanel.classList.toggle("hidden", !opening);
  btnEq.classList.toggle("active", opening);
  btnEq.setAttribute("aria-expanded", String(opening));
});

// Горячие клавиши: пробел — play/pause, стрелки — треки. Не срабатывают
// из полей ввода, чтобы пробел в поиске не ставил музыку на паузу.
document.addEventListener("keydown", (e) => {
  const tag = (e.target as HTMLElement).tagName;
  if (tag === "INPUT" || tag === "TEXTAREA") return;
  if ($("app").classList.contains("hidden")) return;
  if (e.code === "Space") { e.preventDefault(); $("btnPlay").click(); }
  else if (e.code === "ArrowRight") { api("/api/player/next").catch(() => {}); }
  else if (e.code === "ArrowLeft") { api("/api/player/prev").catch(() => {}); }
});

// --- оценки ---
//
// Лайк/дизлайк идут на текущий трек из последнего кадра SSE. Дизлайк — это
// усиленный скип: сервер сам учит волну и переводит очередь дальше
// (handleDislike в routes.go), звать next отсюда дополнительно не нужно.
$("btnLike").addEventListener("click", () => {
  const t = lastState?.track;
  if (!t) return;
  const method = t.liked ? "DELETE" : "POST";
  api(`/api/tracks/${t.id}/like`, undefined, method).catch(() => {});
});
$("btnDislike").addEventListener("click", () => {
  const t = lastState?.track;
  if (!t) return;
  api(`/api/tracks/${t.id}/dislike`).catch(() => {});
});

// --- прогресс ---

// Последний отрисованный кадр состояния — нужен не для отрисовки, а чтобы
// сохранять контекст воспроизведения (см. saveResumeContext выше).
let lastState: State | null = null;

// fmtTime — единственный формат длительности в интерфейсе (m:ss).
function fmtTime(sec: number): string {
  if (!isFinite(sec) || sec <= 0) return "0:00";
  const m = Math.floor(sec / 60);
  const s = Math.floor(sec % 60);
  return `${m}:${String(s).padStart(2, "0")}`;
}

// setProgress двигает и сам ползунок, и закрашенную часть дорожки
// (CSS-переменная --fill: webkit-дорожку нельзя закрасить псевдоэлементом
// прогресса, как в Firefox, поэтому градиент по переменной).
function setProgress(pct: number): void {
  const v = Math.max(0, Math.min(100, pct));
  progressEl.value = String(v);
  progressEl.style.setProperty("--fill", v + "%");
}

// Пока ползунок держат, фоновые обновления не должны выдирать его из-под
// курсора: pointerdown/pointerup обрамляют жест перетаскивания.
let progressDragging = false;
const progressEl = $<HTMLInputElement>("progress");

progressEl.addEventListener("pointerdown", () => { progressDragging = true; });
progressEl.addEventListener("pointerup", () => { progressDragging = false; });
progressEl.addEventListener("change", () => { progressDragging = false; });

progressEl.addEventListener("input", () => {
  const pct = parseFloat(progressEl.value);
  setProgress(pct);
  if (audio.duration) {
    audio.currentTime = (pct / 100) * audio.duration;
    $("timeCur").textContent = fmtTime(audio.currentTime);
    $("bigTime").textContent = fmtTime(audio.currentTime);
  }
});

// Полосу движет rAF-цикл на время воспроизведения: currentTime читается
// живым значением каждый кадр, и полоса ползёт непрерывно, а не скачками
// на 4 Гц от timeupdate. Сам timeupdate остаётся как запасной путь для
// перемотки на паузе — там rAF не крутится.
// Позицию на сервер шлём отдельно и реже: серверу чаще не нужно.
let progressRaf = 0;
function tickProgress(): void {
  progressRaf = 0;
  if (!progressDragging && audio.duration) {
    setProgress((audio.currentTime / audio.duration) * 100);
    $("timeCur").textContent = fmtTime(audio.currentTime);
    $("bigTime").textContent = fmtTime(audio.currentTime);
  }
  if (!audio.paused && !audio.ended) {
    progressRaf = requestAnimationFrame(tickProgress);
  }
}
function startProgressLoop(): void {
  if (progressRaf === 0) progressRaf = requestAnimationFrame(tickProgress);
}
audio.addEventListener("playing", startProgressLoop);
audio.addEventListener("timeupdate", () => {
  if (progressDragging || progressRaf !== 0) return; // на ходу полосу ведёт rAF
  if (audio.duration) {
    setProgress((audio.currentTime / audio.duration) * 100);
    $("timeCur").textContent = fmtTime(audio.currentTime);
    $("bigTime").textContent = fmtTime(audio.currentTime);
  }
});

setInterval(() => {
  if (!audio.paused && audio.currentTime > 0) {
    // trackId — сервер игнорирует тик, если он больше не относится к
    // текущему треку очереди (гонка с ручным переключением, routes.go
    // handleProgress).
    api("/api/player/progress", { position: audio.currentTime, trackId: currentTrackId }).catch(() => {});
    saveResumeContext();
  }
}, 5000);

// --- сбои воспроизведения ---

// Считаем подряд идущие сбои элемента <audio>, а не статус из SSE: сервер
// отражает то, что ему сказал клиент, и может показывать "playing" в
// момент, когда сам элемент уже сломался. Успешный старт (событие "playing"
// самого <audio>) сбрасывает счётчик. Без предела на нём — при отсутствии
// Плюса или подписки — плеер начал бы долбить API, перебирая всю очередь
// на максимальной скорости.
let consecutiveFailures = 0;

// Сетевые откаты (спека §10 «сеть пропала»): обрыв сети — не отказ трека.
// Поток не скипаем: сеть вернётся, и трек продолжится с той же позиции.
// Перезапуск идёт с экспоненциальным откатом; до его срабатывания доигрывает
// то, что уже лежит в буфере <audio>.
const netErrorMsg = "Нет соединения — воспроизведение восстановится автоматически";
let netRetryDelay = 1000;
const maxNetRetryDelay = 30000;
let netRetryTimer: number | undefined;
let netRetryActive = false;
let resumePosOnRetry = 0;

// Успешный старт снимает и счётчик, и сетевой откат, и устойчивое сообщение
// о пределе — все они описывают одно условие ("воспроизведение не идёт"),
// и все снимаются одним событием, которое доказывает, что оно снято.
audio.addEventListener("playing", () => {
  consecutiveFailures = 0;
  netRetryActive = false;
  netRetryDelay = 1000;
  window.clearTimeout(netRetryTimer);
  setFailureError("");
});

// Естественное окончание трека обязано пометить себя reason:"finished":
// серверский дефолт — в пользу человека (любой next без маркера — скип,
// routes.go, handleNext), и без него каждый доигранный трек волны учил бы
// ротор ровно наоборот. Ручные кнопки next/prev reason не шлют — это скип.
audio.addEventListener("ended", () => { api("/api/player/next", { reason: "finished" }).catch(() => {}); });

audio.addEventListener("error", () => {
  // src убирается намеренно, когда трека больше нет в состоянии
  // (render → !t) — это не сбой воспроизведения, и считать его не нужно.
  if (!currentTrackId) return;
  // Демон слушает localhost, поэтому при отвале интернета <audio> получает
  // 502 прокси (SRC_NOT_SUPPORTED), а не сетевую ошибку элемента — судим по
  // navigator.onLine, а не только по коду.
  if (navigator.onLine === false || audio.error?.code === MediaError.MEDIA_ERR_NETWORK) {
    scheduleNetRetry();
    return;
  }
  consecutiveFailures++;
  if (consecutiveFailures >= 3) {
    setFailureError("Не удаётся воспроизвести. Проверьте подписку и токен.");
    return;
  }
  // Сноска о скипе ошибочного трека (спека §10: «пометить и идти дальше»).
  showTransientError("Трек не загрузился — пропускаем");
  api("/api/player/next").catch(() => {});
});

function scheduleNetRetry(): void {
  // Позицию запоминаем в момент обрыва: после load() элемент обнуляет
  // currentTime, и перезаписывать сохранённое значение нулём нельзя.
  if (audio.currentTime > 0) resumePosOnRetry = audio.currentTime;
  netRetryActive = true;
  setFailureError(netErrorMsg);
  window.clearTimeout(netRetryTimer);
  netRetryTimer = window.setTimeout(retryStream, netRetryDelay);
  netRetryDelay = Math.min(netRetryDelay * 2, maxNetRetryDelay);
}

// retryStream перезагружает поток и встаёт на запомненную позицию. Если
// сети всё ещё нет, ничего не дёргаем — нас разбудит событие online.
function retryStream(): void {
  if (!netRetryActive || !currentTrackId) return;
  if (navigator.onLine === false) return;
  const onCanPlay = (): void => {
    audio.removeEventListener("canplay", onCanPlay);
    audio.currentTime = resumePosOnRetry;
    audio.play().catch(() => {});
  };
  audio.addEventListener("canplay", onCanPlay);
  audio.load();
}

window.addEventListener("offline", () => {
  // Доигрываем буфер — звук не прерываем; индикатор снимет "playing"
  // (если буфера хватило до возврата сети) или online-ветка ниже.
  setFailureError(netErrorMsg);
});

window.addEventListener("online", () => {
  netRetryDelay = 1000;
  if (netRetryActive) {
    retryStream();
  } else if (stickyFailureError === netErrorMsg) {
    // Буфера хватило: звук не прерывался, индикатор просто снимаем.
    setFailureError("");
  }
});

checkAuth().then((ok) => { if (ok) connect(); });
