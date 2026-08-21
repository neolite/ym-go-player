package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"music212/internal/player"
	"music212/internal/stream"
	"music212/internal/ymapi"
)

// App связывает все модули и отдаёт роутер.
type App struct {
	Auth   *Auth
	Queue  *player.Queue
	Hub    *Hub
	Buffer *stream.Buffer
	Proxy  *stream.Proxy
	Client func() (*ymapi.Client, error)

	mu       sync.RWMutex
	status   player.Status
	position float64
	volume   float64
	errText  string
	batchID  string
}

// Routes собирает роутер демона.
func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	if a.Auth != nil {
		a.Auth.Register(mux)
	}
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("GET /api/search", a.handleSearch)
	mux.HandleFunc("GET /api/playlists", a.handlePlaylists)
	mux.HandleFunc("GET /api/playlists/{kind}", a.handlePlaylistTracks)
	mux.HandleFunc("GET /api/likes", a.handleLikes)
	mux.HandleFunc("POST /api/play", a.handlePlay)
	mux.HandleFunc("POST /api/player/next", a.handleNext)
	mux.HandleFunc("POST /api/player/prev", a.handlePrev)
	mux.HandleFunc("POST /api/player/pause", a.handlePause)
	mux.HandleFunc("POST /api/player/resume", a.handleResume)
	mux.HandleFunc("POST /api/player/progress", a.handleProgress)
	mux.HandleFunc("POST /api/player/volume", a.handleVolume)
	mux.HandleFunc("GET /stream/{trackId}", a.handleStream)
	return mux
}

// OriginGuard отсекает запросы, пришедшие со сторонних страниц.
//
// Демон слушает только 127.0.0.1, но это не защищает от чужой вкладки в том
// же браузере: она знает адрес и порт и может отправить простой запрос
// (например, POST без нестандартных заголовков) в обход preflight-проверки
// CORS, которая прикрывает только запросы с нестандартными заголовками.
// Заголовок Origin браузер подставляет сам, со страницы его подделать
// нельзя, поэтому сверка с собственным адресом демона (r.Host) отсекает
// такие запросы. Обычные переходы, <audio> и curl заголовок Origin не
// ставят вовсе — их пропускаем, не имея права сломать.
//
// Оформлена как отдельная функция-обёртка, а не метод App: задача 14
// сначала домонтирует к роутеру раздачу статики по пути "/", и только потом
// оборачивает всё целиком — метод, прячущий Routes() внутри себя, такой
// сборки не допустил бы.
func OriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://"+r.Host {
			http.Error(w, "запрос со стороннего источника отклонён", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// goSafe выполняет фоновую работу так, чтобы её сбой не уронил демон.
// Фидбек ротора и статистика прослушиваний необязательны для
// воспроизведения: их ошибки попадают в лог (с пометкой what), но никогда
// не прерывают проигрывание. Паника внутри горутины, запущенной обычным
// go-вызовом, в отличие от паники в HTTP-обработчике, ничем не
// перехватывается и валит весь процесс — поэтому все фоновые вызовы этого
// класса обязаны идти через этот помощник, а не через голый go.
func goSafe(what string, fn func() error) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("%s: паника в фоновой задаче: %v", what, r)
			}
		}()
		if err := fn(); err != nil {
			log.Printf("%s: %v", what, err)
		}
	}()
}

// snapshot собирает текущее состояние для отправки фронтенду.
func (a *App) snapshot() player.State {
	tracks, idx, source := a.Queue.Snapshot()
	a.mu.RLock()
	defer a.mu.RUnlock()

	st := player.State{
		Status:     a.status,
		Position:   a.position,
		Volume:     a.volume,
		Queue:      tracks,
		QueueIndex: idx,
		Source:     source,
		Error:      a.errText,
	}
	if st.Status == "" {
		st.Status = player.StatusIdle
	}
	if cur := a.Queue.Current(); cur != nil {
		st.Track = cur
		st.Duration = float64(cur.Duration)
	}
	return st
}

func (a *App) publish() { a.Hub.Broadcast(a.snapshot()) }

func (a *App) setStatus(s player.Status, errText string) {
	a.mu.Lock()
	a.status, a.errText = s, errText
	a.mu.Unlock()
	a.publish()
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	a.Hub.HandleSSE(w, r, a.snapshot())
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустой запрос"})
		return
	}
	tracks, err := c.SearchTracks(r.Context(), text)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (a *App) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	uid, ok := a.requireUID(w)
	if !ok {
		return
	}
	lists, err := c.UserPlaylists(r.Context(), uid)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, lists)
}

func (a *App) handlePlaylistTracks(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	kind, convErr := strconv.Atoi(r.PathValue("kind"))
	if convErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неверный kind"})
		return
	}
	uid, ok := a.requireUID(w)
	if !ok {
		return
	}
	tracks, err := c.PlaylistTracks(r.Context(), uid, kind)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

func (a *App) handleLikes(w http.ResponseWriter, r *http.Request) {
	c, err := a.client(w)
	if err != nil {
		return
	}
	uid, ok := a.requireUID(w)
	if !ok {
		return
	}
	tracks, err := c.LikedTracks(r.Context(), uid)
	if err != nil {
		a.apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tracks)
}

// handlePlay строит очередь из указанного источника.
func (a *App) handlePlay(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string         `json:"source"`
		Kind   int            `json:"kind"`
		Query  string         `json:"query"`
		Tracks []player.Track `json:"tracks"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать запрос"})
		return
	}
	c, err := a.client(w)
	if err != nil {
		return
	}

	var tracks []player.Track
	switch body.Source {
	case "wave":
		batch, wErr := c.StationTracks(r.Context(), ymapi.WaveStationID, "")
		if wErr != nil {
			a.apiError(w, wErr)
			return
		}
		a.mu.Lock()
		a.batchID = batch.BatchID
		a.mu.Unlock()
		tracks = batch.Tracks
		goSafe("ротор: radioStarted", func() error {
			return c.RotorFeedback(context.Background(), ymapi.WaveStationID, batch.BatchID, ymapi.EventRadioStarted, "", 0)
		})
	case "playlist":
		uid, ok := a.requireUID(w)
		if !ok {
			return
		}
		tracks, err = c.PlaylistTracks(r.Context(), uid, body.Kind)
	case "likes":
		uid, ok := a.requireUID(w)
		if !ok {
			return
		}
		tracks, err = c.LikedTracks(r.Context(), uid)
	case "search":
		tracks, err = c.SearchTracks(r.Context(), body.Query)
	case "tracks":
		tracks = body.Tracks
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "неизвестный источник"})
		return
	}
	if err != nil {
		a.apiError(w, err)
		return
	}
	if len(tracks) == 0 {
		writeJSON(w, http.StatusOK, a.snapshot())
		return
	}

	a.Queue.Set(tracks, body.Source)
	a.retainBuffer()
	a.setStatus(player.StatusLoading, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleNext(w http.ResponseWriter, r *http.Request) {
	a.reportFinished(ymapi.EventTrackFinished)
	if !a.Queue.Next() {
		a.setStatus(player.StatusIdle, "")
		writeJSON(w, http.StatusOK, a.snapshot())
		return
	}
	a.refillWave(r.Context())
	a.retainBuffer()
	a.setStatus(player.StatusLoading, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handlePrev(w http.ResponseWriter, r *http.Request) {
	a.Queue.Prev()
	a.retainBuffer()
	a.setStatus(player.StatusLoading, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handlePause(w http.ResponseWriter, r *http.Request) {
	a.setStatus(player.StatusPaused, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleResume(w http.ResponseWriter, r *http.Request) {
	a.setStatus(player.StatusPlaying, "")
	writeJSON(w, http.StatusOK, a.snapshot())
}

// handleProgress принимает позицию от фронтенда: воспроизведением владеет он.
func (a *App) handleProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Position float64 `json:"position"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)
	a.mu.Lock()
	a.position = body.Position
	a.status = player.StatusPlaying
	a.mu.Unlock()
	a.publish()
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleVolume(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Volume float64 `json:"volume"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать запрос"})
		return
	}
	if body.Volume < 0 {
		body.Volume = 0
	}
	if body.Volume > 1 {
		body.Volume = 1
	}
	a.mu.Lock()
	a.volume = body.Volume
	a.mu.Unlock()
	a.publish()
	writeJSON(w, http.StatusOK, a.snapshot())
}

func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	if a.Proxy == nil {
		http.Error(w, "прокси не настроен", http.StatusServiceUnavailable)
		return
	}
	a.Proxy.ServeTrack(w, r, r.PathValue("trackId"))
}

// retainBuffer оставляет в буфере только текущий и следующий трек.
func (a *App) retainBuffer() {
	if a.Buffer == nil {
		return
	}
	tracks, idx, _ := a.Queue.Snapshot()
	keep := make([]string, 0, 2)
	for i := idx; i < idx+2 && i < len(tracks); i++ {
		keep = append(keep, tracks[i].ID)
	}
	a.Buffer.Retain(keep...)
}

// refillWave подкачивает следующий батч ротора, когда очередь подходит к концу.
func (a *App) refillWave(ctx context.Context) {
	_, _, source := a.Queue.Snapshot()
	if source != "wave" || a.Queue.Remaining() >= 2 {
		return
	}
	c, err := a.newClient()
	if err != nil {
		return
	}
	cur := a.Queue.Current()
	last := ""
	if cur != nil {
		last = cur.ID
	}
	batch, err := c.StationTracks(ctx, ymapi.WaveStationID, last)
	if err != nil || batch == nil {
		return
	}
	a.mu.Lock()
	a.batchID = batch.BatchID
	a.mu.Unlock()
	a.Queue.Append(batch.Tracks)
}

// reportFinished отправляет оба канала фидбека для текущего трека. Оба
// вызова асинхронны и идут через goSafe: фидбек ротора и статистика
// прослушиваний необязательны для воспроизведения и не должны ни прерывать
// его, ни ронять демон, если Яндекс перестанет их принимать.
func (a *App) reportFinished(event string) {
	cur := a.Queue.Current()
	if cur == nil {
		return
	}
	c, err := a.newClient()
	if err != nil {
		return
	}
	a.mu.RLock()
	pos, batch := a.position, a.batchID
	a.mu.RUnlock()

	_, _, source := a.Queue.Snapshot()
	if source == "wave" {
		goSafe("ротор: фидбек трека", func() error {
			return c.RotorFeedback(context.Background(), ymapi.WaveStationID, batch, event, cur.ID, pos)
		})
	}
	goSafe("статистика: playAudio", func() error {
		return c.PlayAudio(context.Background(), ymapi.PlayEvent{
			TrackID:       cur.ID,
			From:          source,
			PlayedSeconds: pos,
			TotalSeconds:  float64(cur.Duration),
		})
	})
}

func (a *App) newClient() (*ymapi.Client, error) {
	if a.Client == nil {
		return nil, ymapi.ErrUnauthorized
	}
	return a.Client()
}

// client достаёт клиент и сам отвечает 401, если токена нет.
func (a *App) client(w http.ResponseWriter) (*ymapi.Client, error) {
	c, err := a.newClient()
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "нужен токен"})
		return nil, err
	}
	return c, nil
}

// requireUID достаёт идентификатор пользователя из проверенного статуса
// аккаунта и сам отвечает 401, если статус ещё не получен. Auth.UID
// возвращает 0 до первой успешной проверки токена и после logout — в этом
// состоянии Auth.Status() равен nil, и разыменовывать его поле напрямую
// нельзя.
func (a *App) requireUID(w http.ResponseWriter) (int64, bool) {
	uid := a.Auth.UID()
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "статус аккаунта ещё не получен"})
		return 0, false
	}
	return uid, true
}

// apiError переводит ошибку ymapi в устойчивый ответ клиенту. Сам текст
// ошибки в ответ не попадает никогда: она может прийти обёрнутой в
// *url.Error, а тот включает полный адрес запроса — наружу должен уходить
// только выбранный по классу ошибки устойчивый текст, подробности идут в
// лог демона.
func (a *App) apiError(w http.ResponseWriter, err error) {
	code := http.StatusBadGateway
	msg := "сервис Яндекс Музыки недоступен"
	switch {
	case errors.Is(err, ymapi.ErrUnauthorized):
		code = http.StatusUnauthorized
		msg = "нужна авторизация — токен недействителен"
	case errors.Is(err, ymapi.ErrForbidden):
		code = http.StatusForbidden
		msg = "доступ запрещён — проверьте регион и подписку"
	}
	log.Printf("httpapi: запрос к Яндекс Музыке завершился ошибкой: %v", err)
	writeJSON(w, code, map[string]string{"error": msg})
}
