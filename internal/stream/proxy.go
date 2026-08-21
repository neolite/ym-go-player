package stream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// maxTrackBytes ограничивает размер одного трека, который мы готовы держать
// в памяти. Это осознанное ограничение версии, а не недосмотр: трек целиком
// живёт в памяти процесса, поэтому длинные записи (подкасты, аудиокниги,
// многочасовые миксы) архитектурно не поддерживаются. 128 МиБ — это около
// 54 минут звучания на 320 кбит/с, с запасом покрывает всю обычную музыку,
// а два таких трека всё ещё умещаются в 256-мегабайтный буфер (Buffer).
const maxTrackBytes = 128 << 20

// Resolver отдаёт свежую подписанную ссылку на трек.
// Реализуется *ymapi.Client.
type Resolver interface {
	ResolveTrack(ctx context.Context, trackID string) (string, error)
}

// pendingFetch — незавершённая загрузка трека, на которую подписались
// параллельные запросы. done закрывается, когда data/err уже можно читать.
type pendingFetch struct {
	done chan struct{}
	data []byte
	err  error
}

// Proxy отдаёт аудио фронтенду, скрывая от него ссылки и токен.
type Proxy struct {
	resolver Resolver
	buf      *Buffer
	http     *http.Client
	// maxBytes — предел размера трека. По умолчанию maxTrackBytes;
	// вынесен в поле, а не оставлен константой, только чтобы тесты могли
	// проверить отказ на маленьких данных, не гоняя сотни мегабайт.
	maxBytes int64

	mu      sync.Mutex
	pending map[string]*pendingFetch
}

// NewProxy собирает прокси. hc может быть nil — тогда берётся клиент по умолчанию.
func NewProxy(r Resolver, b *Buffer, hc *http.Client) *Proxy {
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Proxy{
		resolver: r,
		buf:      b,
		http:     hc,
		maxBytes: maxTrackBytes,
		pending:  make(map[string]*pendingFetch),
	}
}

// ServeTrack отдаёт трек с поддержкой перемотки.
// Загруженный трек кладётся в буфер, поэтому повторные запросы и перемотка
// не порождают новых обращений к Яндексу.
func (p *Proxy) ServeTrack(w http.ResponseWriter, req *http.Request, trackID string) {
	data, ok := p.buf.Get(trackID)
	if !ok {
		var err error
		data, err = p.fetchOnce(req.Context(), trackID)
		if err != nil {
			// Подробности (в т.ч. подписанную ссылку внутри *url.Error) —
			// только в лог демона. Клиенту уходит устойчивый текст без них.
			log.Printf("stream: не удалось получить трек %s: %v", trackID, err)
			http.Error(w, "не удалось получить трек", http.StatusBadGateway)
			return
		}
	}
	// Content-Type проставлен жёстко: Resolver отдаёт только ссылку, кодек
	// (MP3/AAC — PickBest мог выбрать любой) прокси не знает. Браузеры
	// определяют формат по содержимому, так что на воспроизведение это не влияет.
	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-store")
	// ServeContent сам разбирает Range и отдаёт 206 — свой разбор диапазонов не нужен.
	http.ServeContent(w, req, trackID+".mp3", time.Time{}, bytes.NewReader(data))
}

// fetchOnce гарантирует, что параллельные запросы одного трека порождают
// не больше одной загрузки: первый пришедший становится «лидером» и качает
// трек, остальные ждут результата на канале done и получают те же байты.
// По завершении запись из pending удаляется — иначе она станет вечной утечкой.
func (p *Proxy) fetchOnce(ctx context.Context, trackID string) ([]byte, error) {
	p.mu.Lock()
	if pf, ok := p.pending[trackID]; ok {
		p.mu.Unlock()
		<-pf.done
		return pf.data, pf.err
	}
	pf := &pendingFetch{done: make(chan struct{})}
	p.pending[trackID] = pf
	p.mu.Unlock()

	pf.data, pf.err = p.fetch(ctx, trackID)
	if pf.err == nil {
		p.buf.Put(trackID, pf.data)
	}

	p.mu.Lock()
	delete(p.pending, trackID)
	p.mu.Unlock()
	close(pf.done)

	return pf.data, pf.err
}

// fetch резолвит ссылку и читает трек целиком.
// При 410 (истёкшая ссылка) делается один повторный резолв.
func (p *Proxy) fetch(ctx context.Context, trackID string) ([]byte, error) {
	for attempt := 0; attempt < 2; attempt++ {
		link, err := p.resolver.ResolveTrack(ctx, trackID)
		if err != nil {
			return nil, err
		}
		data, status, err := p.download(ctx, link)
		if err == nil {
			return data, nil
		}
		if status == http.StatusGone && attempt == 0 {
			continue // ссылка протухла — берём свежую
		}
		return nil, err
	}
	// Цикл со счётчиком не считается в Go завершающим оператором — без этого
	// return файл не скомпилируется, хотя фактически сюда не дойти.
	return nil, fmt.Errorf("ссылка истекает быстрее, чем удаётся её использовать")
}

// oversizedTrackMsg — человеко-читаемое объяснение отказа по размеру.
const oversizedTrackMsg = "трек слишком велик для буфера в памяти: " +
	"длинные записи (подкасты, аудиокниги, многочасовые миксы) в этой версии не поддерживаются"

func (p *Proxy) download(ctx context.Context, link string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("источник вернул %d", resp.StatusCode)
	}
	// Content-Length известен заранее — отказываем сразу, не выкачивая
	// впустую сотню мегабайт, если источник честно сообщил размер.
	if resp.ContentLength > p.maxBytes {
		return nil, resp.StatusCode, fmt.Errorf("%s", oversizedTrackMsg)
	}
	// Читаем с запасом в один байт: LimitReader, упёршись в предел, отдаёт
	// io.EOF без ошибки, и ReadAll вернул бы ровно maxBytes с ошибкой nil —
	// то есть молча отдал бы обрезок трека вместо честного отказа.
	data, err := io.ReadAll(io.LimitReader(resp.Body, p.maxBytes+1))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if int64(len(data)) > p.maxBytes {
		return nil, resp.StatusCode, fmt.Errorf("%s", oversizedTrackMsg)
	}
	return data, resp.StatusCode, nil
}
