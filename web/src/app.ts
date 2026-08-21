// Фронтенд владеет воспроизведением; демон владеет очередью и фидбеком.
// Состояние приходит по SSE и здесь не дублируется: страница только рисует.
// Единственное, что живёт на клиенте, — то, чего нет в состоянии: флаг
// перетаскивания ползунка, счётчик неудачных попыток воспроизведения,
// пауза переподключения.

type Status = "idle" | "loading" | "playing" | "paused" | "error";

interface Track {
  id: string; title: string; artists: string[];
  album: string; coverUrl: string; duration: number; available: boolean;
}

interface State {
  status: Status; track: Track | null; position: number; duration: number;
  volume: number; queue: Track[]; queueIndex: number; source: string; error?: string;
}

const $ = <T extends HTMLElement>(id: string) => document.getElementById(id) as T;

const audio = $<HTMLAudioElement>("audio");
const listEl = $<HTMLUListElement>("list");
const errorBar = $<HTMLDivElement>("errorBar");
let currentTrackId = "";

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
//   пока его явно не снимут: SSE-кадры идут по изменению состояния, а не
//   периодически, так что для "устойчивой" ошибки никто не пришлёт её
//   заново, и таймер, стирающий её через 4с, вернёт пользователя в то самое
//   молчаливо сломанное состояние, ради избежания которого её вообще
//   показывали. Отдельные слоты для двух устойчивых источников — потому
//   что у них разные условия снятия (новый кадр без error / событие
//   "playing"), и один частый источник (SSE-кадр по другому поводу) не
//   должен затирать сообщение другого.
//
// Разовая ошибка может временно перекрыть устойчивую — после её
// автоскрытия строка возвращается к устойчивой, если та ещё в силе.

let stickyStateError = "";
let stickyFailureError = "";
let transientHideTimer: number | undefined;

function currentSticky(): string {
  // Сообщение о пределе неудач актуальнее: оно объясняет, почему плеер
  // молчит прямо сейчас, даже если параллельно пришёл кадр SSE без error.
  return stickyFailureError || stickyStateError;
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

// Все маршруты, которые вызывает api(), — POST (routes.go:46-52); GET-роуты
// (/api/playlists, /api/likes, /api/search) идут отдельным fetch мимо этой
// функции. method обязан быть POST всегда, а не только когда есть тело:
// fetch(path, {}) без явного method уходит GET, и без тела (next/prev/
// pause/resume) все эти вызовы получали 404.
async function api(path: string, body?: unknown): Promise<any> {
  const res = await fetch(path, body === undefined
    ? { method: "POST" }
    : {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
  if (!res.ok) {
    const text = await res.text();
    showTransientError(text);
    throw new Error(text);
  }
  return res.status === 204 ? null : res.json();
}

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
    $("auth").classList.add("hidden");
    $("app").classList.remove("hidden");
    return true;
  }
  $("auth").classList.remove("hidden");
  $("app").classList.add("hidden");
  $("authMsg").textContent = st.message ?? "";
  ($("authLink") as HTMLAnchorElement).href = st.authUrl ?? "#";
  return false;
}

$("tokenSave").addEventListener("click", async () => {
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

// --- отрисовка ---

function render(st: State): void {
  const t = st.track;
  // Оборона клиента: сервер обязан слать [] вместо null для срезов
  // состояния (см. internal/player/queue.go), но один разорванный кадр не
  // должен обрывать отрисовку на середине — отсюда `?? []` на каждом
  // массиве состояния, а не только на верхнем st.queue.
  const queue = st.queue ?? [];
  $("title").textContent = t ? t.title : "—";
  $("artist").textContent = t ? (t.artists ?? []).join(", ") : "";
  ($("cover") as HTMLImageElement).src = t?.coverUrl ?? "";
  $<HTMLButtonElement>("btnPlay").textContent = st.status === "playing" ? "⏸" : "▶";

  // st.error показываем независимо от того, есть трек или нет — ошибка
  // источника (например, отсутствие Плюса) не обязана ждать пустого трека.
  // Устойчивая ошибка, а не разовая: держится, пока кадр её несёт, и
  // снимается ровно тем кадром, где error снова пуст.
  setStateError(st.error ?? "");

  listEl.innerHTML = "";
  queue.forEach((track, i) => {
    const li = document.createElement("li");
    li.textContent = `${track.title} — ${(track.artists ?? []).join(", ")}`;
    if (i === st.queueIndex) li.className = "current";
    li.addEventListener("click", () => {
      api("/api/play", { source: "tracks", tracks: queue.slice(i) }).catch(() => {});
    });
    listEl.appendChild(li);
  });

  // Смена трека — единственный повод трогать src: иначе перезапустим текущий.
  if (t && t.id !== currentTrackId) {
    currentTrackId = t.id;
    audio.src = `/stream/${t.id}`;
    audio.play().catch(() => {});
    updateMediaSession(t);
  }
  if (!t) { currentTrackId = ""; audio.removeAttribute("src"); }
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
  subscribe(source, render);
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
  const lists = await (await fetch("/api/playlists")).json();
  listEl.innerHTML = "";
  for (const pl of lists) {
    const li = document.createElement("li");
    li.textContent = `${pl.title} (${pl.trackCount})`;
    li.addEventListener("click", () => {
      api("/api/play", { source: "playlist", kind: pl.kind }).catch(() => {});
    });
    listEl.appendChild(li);
  }
});

$("search").addEventListener("keydown", (e) => {
  if ((e as KeyboardEvent).key !== "Enter") return;
  const q = ($("search") as HTMLInputElement).value.trim();
  if (q) api("/api/play", { source: "search", query: q }).catch(() => {});
});

$("volume").addEventListener("input", () => {
  const v = parseFloat(($("volume") as HTMLInputElement).value);
  audio.volume = v;
  api("/api/player/volume", { volume: v }).catch(() => {});
});

// --- прогресс ---

// Пока ползунок держат, фоновые обновления не должны выдирать его из-под
// курсора: pointerdown/pointerup обрамляют жест перетаскивания.
let progressDragging = false;
const progressEl = $<HTMLInputElement>("progress");

progressEl.addEventListener("pointerdown", () => { progressDragging = true; });
progressEl.addEventListener("pointerup", () => { progressDragging = false; });
progressEl.addEventListener("change", () => { progressDragging = false; });

progressEl.addEventListener("input", () => {
  const pct = parseFloat(progressEl.value);
  if (audio.duration) audio.currentTime = (pct / 100) * audio.duration;
});

// Полосу двигаем на timeupdate — он приходит по несколько раз в секунду,
// так что глаз не видит скачков. Позицию на сервер шлём отдельно и реже:
// серверу чаще не нужно.
audio.addEventListener("timeupdate", () => {
  if (progressDragging) return;
  if (audio.duration) {
    progressEl.value = String((audio.currentTime / audio.duration) * 100);
  }
});

setInterval(() => {
  if (!audio.paused && audio.currentTime > 0) {
    api("/api/player/progress", { position: audio.currentTime }).catch(() => {});
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

// Успешный старт снимает и счётчик, и устойчивое сообщение о пределе —
// оба описывают одно и то же условие ("воспроизведение не идёт"), и оба
// снимаются одним и тем же событием, которое доказывает, что оно снято.
audio.addEventListener("playing", () => {
  consecutiveFailures = 0;
  setFailureError("");
});

audio.addEventListener("ended", () => { api("/api/player/next").catch(() => {}); });

audio.addEventListener("error", () => {
  // src убирается намеренно, когда трека больше нет в состоянии
  // (render → !t) — это не сбой воспроизведения, и считать его не нужно.
  if (!currentTrackId) return;
  consecutiveFailures++;
  if (consecutiveFailures >= 3) {
    setFailureError("Не удаётся воспроизвести. Проверьте подписку и токен.");
    return;
  }
  api("/api/player/next").catch(() => {});
});

checkAuth().then((ok) => { if (ok) connect(); });
