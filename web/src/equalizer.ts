// Пятиполосный эквалайзер поверх Web Audio API.
//
// Граф (MediaElementSourceNode → 5×BiquadFilterNode → destination) создаётся
// лениво, при первом открытии панели (initEqualizer), а не при загрузке
// страницы: до пользовательского жеста браузер всё равно создал бы
// AudioContext в состоянии suspended, и заводить эту сущность раньше, чем
// панель хоть раз откроют, незачем.
//
// createMediaElementSource бросает исключение при повторном вызове на одном
// и том же <audio> — граф поэтому строится один раз за жизнь страницы.
// Смена трека переприсваивает audio.src самому элементу, а не пересоздаёт
// его, так что граф переживает переключение треков без переподключения.

export const BAND_FREQS = [60, 230, 910, 3600, 14000];

const EQ_KEY = "music212.eq";
const FLAT = (): number[] => BAND_FREQS.map(() => 0);

function loadGains(): number[] {
  try {
    const raw = localStorage.getItem(EQ_KEY);
    if (!raw) return FLAT();
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed) || parsed.length !== BAND_FREQS.length) return FLAT();
    return parsed.map((v: unknown) => (typeof v === "number" && isFinite(v) ? v : 0));
  } catch {
    return FLAT(); // битая запись или приватный режим — стартуем с плоской АЧХ
  }
}

function saveGains(g: number[]): void {
  try { localStorage.setItem(EQ_KEY, JSON.stringify(g)); } catch { /* приватный режим — не запомнится */ }
}

let gains = loadGains();
let ctx: AudioContext | null = null;
let filters: BiquadFilterNode[] = [];

// initEqualizer — точка входа панели: создаёт граф при первом вызове,
// на повторных лишь снимает suspended (автоплей-политика браузера может
// приостановить контекст между открытиями панели).
export function initEqualizer(audio: HTMLAudioElement): void {
  if (ctx) {
    if (ctx.state === "suspended") ctx.resume().catch(() => {});
    return;
  }
  ctx = new AudioContext();
  const source = ctx.createMediaElementSource(audio);
  filters = BAND_FREQS.map((freq) => {
    const f = ctx!.createBiquadFilter();
    f.type = "peaking";
    f.frequency.value = freq;
    f.Q.value = 1;
    return f;
  });
  filters.forEach((f, i) => { f.gain.value = gains[i]; });
  let node: AudioNode = source;
  for (const f of filters) {
    node.connect(f);
    node = f;
  }
  node.connect(ctx.destination);
}

export function getGains(): number[] {
  return gains.slice();
}

export function setBandGain(i: number, db: number): void {
  gains[i] = db;
  saveGains(gains);
  if (filters[i]) filters[i].gain.value = db;
}

export function resetGains(): void {
  gains = FLAT();
  saveGains(gains);
  filters.forEach((f) => { f.gain.value = 0; });
}
