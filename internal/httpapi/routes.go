package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

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
	mux.HandleFunc("POST /api/player/queue-index", a.handleQueueIndex)
	mux.HandleFunc("POST /api/player/pause", a.handlePause)
	mux.HandleFunc("POST /api/player/resume", a.handleResume)
	mux.HandleFunc("POST /api/player/progress", a.handleProgress)
	mux.HandleFunc("POST /api/player/volume", a.handleVolume)
	mux.HandleFunc("POST /api/tracks/{trackId}/like", a.handleLike)
	mux.HandleFunc("DELETE /api/tracks/{trackId}/like", a.handleUnlike)
	mux.HandleFunc("POST /api/tracks/{trackId}/dislike", a.handleDislike)
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
	// Очередь и текущий трек читаются одним атомарным снимком: раздельные
	// Snapshot() + Current() дали бы кадр, смешивающий два момента времени.
	tracks, idx, source, cur := a.Queue.SnapshotWithCurrent()
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
	if cur != nil {
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
	a.resetPosition()
	// Недоступные в регионе треки пропускаем сразу, не доводя до ошибки
	// <audio> на фронтенде: очередь продолжается, пользователь получает
	// сноску (спека §10). Очередь из одних недоступных — честный idle.
	skipped, landed := a.skipUnavailable()
	if !landed {
		a.setStatus(player.StatusIdle, "")
		if len(skipped) > 0 {
			a.setWarning(skipWarningText(skipped))
		}
		writeJSON(w, http.StatusOK, a.snapshot())
		return
	}
	a.retainBuffer()
	a.prefetchNext()
	a.setStatus(player.StatusLoading, "")
	// trackStarted обязан уйти для каждого трека волны, включая первый —
	// раньше (находка 1) он не уходил вовсе. reportTrackStarted сама
	// проверяет источник и не шлёт ничего для "playlist"/"likes"/"search"/
	// "tracks": там нет станции, которой это событие было бы адресовано.
	a.reportTrackStarted()
	if len(skipped) > 0 {
		a.setWarning(skipWarningText(skipped))
	}
	writeJSON(w, http.StatusOK, a.snapshot())
}

// handleNext переводит очередь на следующий трек. Этот обработчик — точка
// входа для ДВУХ разных пользовательских намерений, которые демон не может
// различить по одной физической природе запроса: нажатие кнопки «вперёд»
// (web/src/app.ts, click на btnNext) и естественное окончание трека
// (audio "ended" в том же файле). Раньше (находка 1) в обоих случаях в
// «Мою волну» безусловно уходил EventTrackFinished — скип на середине
// трека учил волну ровно наоборот: «этот трек понравился».
//
// Тело запроса необязательное: {"reason": "finished"} — явный маркер
// автоперехода; любое другое значение, отсутствие поля или отсутствие
// тела целиком трактуются как скип. Это осознанный дефолт в пользу
// человека, а не default-разбора: кнопку жмёт человек, а автопереход
// обязан пометить себя явно. Ошибка разбора тела (не считая пустого тела)
// не блокирует запрос — тоже трактуется как скип, тот же безопасный
// дефолт для любого не-"finished" случая.
//
// Фронтенд шлёт reason:"finished" из обработчика "ended" <audio>;
// ручные кнопки «вперёд»/«назад», MediaSession и клавиши идут без
// reason и поэтому корректно учитываются как скип.
func (a *App) handleNext(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body)
	event := ymapi.EventSkip
	if body.Reason == "finished" {
		event = ymapi.EventTrackFinished
	}
	a.advanceToNext(r.Context(), event)
	writeJSON(w, http.StatusOK, a.snapshot())
}

// advanceToNext переводит очередь на следующий трек. Общий хвост handleNext
// и handleDislike: дизлайк — это усиленный скип, и весь фидбек уходящего
// трека (event + play-audio внутри reportFinished) обязан уйти ДО сдвига
// очереди, иначе cur и позиция будут относиться уже к новому треку.
//
// reportFinished зовётся только отсюда — то есть только при движении
// ВПЕРЁД. handlePrev и handleQueueIndex фидбек уходящего трека не шлют
// осознанно: возврат назад или клик по треку очереди — не скип и не
// дослушивание, и учить ротор «не нравится» по такому переходу нельзя.
func (a *App) advanceToNext(ctx context.Context, event string) {
	a.reportFinished(event)

	if !a.Queue.Next() {
		a.resetPosition()
		a.setStatus(player.StatusIdle, "")
		return
	}
	// Позиция принадлежит уже ПРОШЛОМУ треку — reportFinished выше уже
	// прочитал её (находка 4). Без обнуления здесь следующий reportFinished
	// (например, второй "next" через секунду) унаследует чужую позицию и
	// пришлёт в оба канала фидбека десятки/сотни секунд для трека, который
	// не звучал вовсе.
	a.resetPosition()
	skipped, landed := a.skipUnavailable()
	if !landed {
		// Хвост очереди — сплошь недоступные треки: играть нечего,
		// но сноску о пропусках пользователь всё равно обязан увидеть.
		a.setStatus(player.StatusIdle, "")
		if len(skipped) > 0 {
			a.setWarning(skipWarningText(skipped))
		}
		return
	}
	a.reportTrackStarted()
	a.refillWave(ctx)
	a.retainBuffer()
	a.prefetchNext()
	a.setStatus(player.StatusLoading, "")
	// Сноска ставится ПОСЛЕ setStatus: тот перезаписывает errText целиком,
	// а предупреждение обязано пережить именно ЭТУ смену статуса. Затрёт
	// его ближайший следующий setStatus — любой: не только переход, но и
	// пауза/возобновление (handlePause/handleResume зовут тот же setStatus).
	if len(skipped) > 0 {
		a.setWarning(skipWarningText(skipped))
	}
}

// handlePrev возвращает очередь на предыдущий трек. skipUnavailable здесь
// нет осознанно: назад идут по явной воле на конкретный трек; если он
// недоступен, ошибку покажет <audio> и сетевой откат фронтенда (спека §10
// применена при движении вперёд). На начале или пустой очереди перехода
// нет — и статус «загрузка» без трека не ставится.
func (a *App) handlePrev(w http.ResponseWriter, r *http.Request) {
	if a.Queue.Prev() {
		a.resetPosition()
		a.reportTrackStarted()
		a.retainBuffer()
		a.prefetchNext()
		a.setStatus(player.StatusLoading, "")
	}
	writeJSON(w, http.StatusOK, a.snapshot())
}

// handleLike ставит лайк: в библиотеку («Мне нравится») синхронно —
// пользователь жмёт кнопку и ждёт подтверждения, ошибку надо показать, —
// а в обучение волны (ротор) в фоне, как весь фидбек станции.
func (a *App) handleLike(w http.ResponseWriter, r *http.Request) {
	a.setTrackLike(w, r, true)
}

// handleUnlike снимает лайк (DELETE /api/tracks/{id}/like по спеке).
// Снятие — действие библиотеки, а не оценка станции, поэтому в ротор
// здесь ничего не уходит.
func (a *App) handleUnlike(w http.ResponseWriter, r *http.Request) {
	a.setTrackLike(w, r, false)
}

func (a *App) setTrackLike(w http.ResponseWriter, r *http.Request, like bool) {
	uid, ok := a.requireUID(w)
	if !ok {
		return
	}
	c, err := a.client(w)
	if err != nil {
		return
	}
	id := r.PathValue("trackId")
	op := c.UnlikeTrack
	if like {
		op = c.LikeTrack
	}
	if err := op(r.Context(), uid, id); err != nil {
		a.apiError(w, err)
		return
	}
	if like {
		a.reportLikeFeedback(id, ymapi.EventLike)
	}
	writeJSON(w, http.StatusOK, a.snapshot())
}

// handleDislike — усиленный скип. Оценка dislike уходит в обучение волны
// (тот же канал, что лайк и скип), а дальше демон делает ровно то же, что
// по кнопке «вперёд»: так себя ведёт и сам Яндекс — дизлайк прерывает трек.
// Оценка не-текущего трека очередь не двигает: перескакивать из-за оценки
// того, что сейчас не звучит, нельзя.
func (a *App) handleDislike(w http.ResponseWriter, r *http.Request) {
	if _, err := a.client(w); err != nil {
		return
	}
	id := r.PathValue("trackId")
	a.reportLikeFeedback(id, ymapi.EventDislike)
	cur := a.Queue.Current()
	if cur == nil || cur.ID != id {
		writeJSON(w, http.StatusOK, a.snapshot())
		return
	}
	a.advanceToNext(r.Context(), ymapi.EventSkip)
	writeJSON(w, http.StatusOK, a.snapshot())
}

// reportLikeFeedback шлёт оценку (like/dislike) в обучение волны — но
// только если оценили именно текущий трек при живой станции: событие
// адресовано станции и её контексту, а не библиотеке. В фоне через goSafe,
// как весь фидбек: сбой канала не должен прерывать воспроизведение.
func (a *App) reportLikeFeedback(trackID, event string) {
	_, _, source := a.Queue.Snapshot()
	if source != "wave" {
		return
	}
	cur := a.Queue.Current()
	if cur == nil || cur.ID != trackID {
		return
	}
	c, err := a.newClient()
	if err != nil {
		// Единственная точка, где оценка обрывается до отправки, — обязана
		// оставить след в логе, как остальной фидбек через goSafe.
		log.Printf("ротор: %s: %v", event, err)
		return
	}
	a.mu.RLock()
	pos, batch := a.position, a.batchID
	a.mu.RUnlock()
	goSafe("ротор: "+event, func() error {
		return c.RotorFeedback(context.Background(), ymapi.WaveStationID, batch, event, trackID, pos)
	})
}

// handleQueueIndex переставляет позицию внутри УЖЕ играющей очереди по
// клику на конкретный трек в списке (находка 2). До этого эндпоинта
// единственным способом перейти внутри очереди было позвать
// POST /api/play {"source":"tracks", "tracks":[...]}: это ставило НОВУЮ
// очередь и меняло source на "tracks" — если играла волна, докачка
// батчей (refillWave) и фидбек ротора (reportFinished) после этого
// прекращались навсегда, потому что оба гейтятся на source == "wave".
// Здесь очередь и её источник не меняются вовсе — двигается только
// позиция, через player.Queue.SetIndex.
//
// Контракт: POST /api/player/queue-index, тело {"index": N} — N считается
// от начала текущей очереди (Queue.Snapshot()[0], тот же массив, что уже
// уходит в кадре SSE как State.Queue). 200 и снапшот состояния при
// удачном переходе, 400 с телом {"error": "..."} — при некорректном или
// нечитаемом теле, а также при индексе вне границ очереди.
func (a *App) handleQueueIndex(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Index int `json:"index"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "не удалось прочитать запрос"})
		return
	}
	if !a.Queue.SetIndex(body.Index) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "индекс вне очереди"})
		return
	}
	a.resetPosition()
	// Клик по недоступному треку — не зависание на ошибке <audio>: идём
	// к ближайшему доступному следом, с сноской (та же спека §10).
	skipped, landed := a.skipUnavailable()
	if !landed {
		a.setStatus(player.StatusIdle, "")
		if len(skipped) > 0 {
			a.setWarning(skipWarningText(skipped))
		}
		writeJSON(w, http.StatusOK, a.snapshot())
		return
	}
	a.reportTrackStarted()
	// Прыжок кликом — такая же смена позиции, что и handleNext: под конец
	// очереди обязаны подкачать следующий батч, иначе волна доиграет
	// остаток и встанет в idle (несимметрия с advanceToNext, находка
	// ре-ревью).
	a.refillWave(r.Context())
	a.retainBuffer()
	a.prefetchNext()
	a.setStatus(player.StatusLoading, "")
	if len(skipped) > 0 {
		a.setWarning(skipWarningText(skipped))
	}
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
// Битое тело молча трактуется как нулевая позиция и статус playing — тики
// идут каждые несколько секунд, и ронять запрос из-за одного битого кадра
// нет смысла: следующий тик принесёт настоящую позицию. По той же причине
// запоздалый тик от уже сменившегося трека может на один кадр перезаписать
// позицию нового — это осознанная цена модели «воспроизведением владеет
// фронтенд», а не упущение.
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

// skipUnavailable продвигает очередь вперёд мимо недоступных в регионе
// треков (Track.Available == false) и возвращает их заголовки для сноски
// в UI. Спека §10: «трек недоступен в регионе — скип со сноской, очередь
// продолжается». Пропуск — не прослушивание: ни trackStarted, ни
// скип-фидбек для такого трека не шлётся, он никогда не был «играющим».
// ok == false означает, что играть нечего: очередь исчерпана (Queue.Next
// отказывается выходить за границу, поэтому позиция остаётся на последнем
// недоступном — только этот флаг отличает «конец» от «стоим на треке»).
func (a *App) skipUnavailable() (skipped []string, ok bool) {
	for {
		cur := a.Queue.Current()
		if cur == nil {
			return skipped, false
		}
		if cur.Available {
			return skipped, true
		}
		title := cur.Title
		if title == "" {
			title = cur.ID
		}
		skipped = append(skipped, title)
		if !a.Queue.Next() {
			// Хвост сплошь недоступен: уводим позицию за конец, чтобы кадр
			// состояния не показывал недоступный трек «текущим».
			a.Queue.Exhaust()
			return skipped, false
		}
	}
}

// skipWarningText — текст сноски о пропущенных недоступных треках.
func skipWarningText(skipped []string) string {
	if len(skipped) == 1 {
		return fmt.Sprintf("Трек «%s» недоступен в вашем регионе — пропущен", skipped[0])
	}
	n := len(skipped)
	word := "треков"
	if n%10 == 1 && n%100 != 11 {
		word = "трек"
	} else if n%10 >= 2 && n%10 <= 4 && (n%100 < 10 || n%100 >= 20) {
		word = "трека"
	}
	return fmt.Sprintf("Недоступны в вашем регионе и пропущены: %d %s", n, word)
}

// prefetchNext запускает упреждающую подкачку следующего трека очереди:
// пока играет текущий (обычно минуты), следующий уже лежит в буфере, и
// переход не ждёт сеть. Зовётся вслед за retainBuffer в каждой точке
// смены позиции — обе функции читают один и тот же срез очереди.
func (a *App) prefetchNext() {
	if a.Proxy == nil {
		return
	}
	tracks, idx, _ := a.Queue.Snapshot()
	if idx+1 >= len(tracks) {
		return
	}
	a.Proxy.Prefetch(tracks[idx+1].ID)
}

// refillWaveBackoff задаёт паузы между повторными попытками подкачки
// батча ротора после первой неудачи. Переменная пакета, а не константа:
// тесты подменяют её на короткие паузы, чтобы не ждать реальное время —
// сама retry-логика (число попыток, нарастание пауз) при этом не меняется.
var refillWaveBackoff = []time.Duration{500 * time.Millisecond, 2 * time.Second}

// refillWaveWarning — текст предупреждения, которое видит пользователь,
// пока идут повторы. Вынесен в переменную, а не встроен в вызовы: и
// setWarning, и последующий clearWarning обязаны сверяться с ОДНИМ и тем
// же текстом (см. clearWarning).
const refillWaveWarning = "не удалось подгрузить продолжение «Моей волны» — повторяем попытку"

// refillWave подкачивает следующий батч ротора, когда очередь подходит к
// концу. Раньше (находка 5) сбой здесь был полностью молчаливым: ни лога,
// ни State.Error, ни повтора — очередь на слабой сети тихо доигрывала до
// конца и останавливалась без единого объяснения. Первая попытка
// синхронная и не меняет задержку ответа обработчика в happy path; если
// она не удалась — предупреждение уходит в State.Error немедленно (а не
// после исчерпания повторов, которое может занять секунды), а сами
// повторы с нарастающей паузой идут в фоне через goSafe и не задерживают
// ответ handleNext/handlePlay пользователю.
func (a *App) refillWave(ctx context.Context) {
	_, _, source := a.Queue.Snapshot()
	if source != "wave" || a.Queue.Remaining() >= 2 {
		return
	}
	c, err := a.newClient()
	if err != nil {
		log.Printf("ротор: подкачка батча волны не удалась: %v", err)
		return
	}
	cur := a.Queue.Current()
	last := ""
	if cur != nil {
		last = cur.ID
	}
	batch, err := c.StationTracks(ctx, ymapi.WaveStationID, last)
	if err == nil && len(batch.Tracks) == 0 {
		// Успех с пустой порцией — тот же отказ, только молчаливый:
		// без этой проверки волна доигрывала остаток очереди и вставала
		// без единого объяснения (StationTracks никогда не возвращает
		// (nil, nil) — живой пустой случай именно такой).
		err = errors.New("ротор вернул пустой батч")
	}
	if err == nil {
		// Пока шёл сетевой запрос, источник мог смениться — дозаливка
		// атомарна с проверкой (AppendIfSource), чтобы батч волны не
		// попал в чужую очередь.
		if !a.Queue.AppendIfSource(batch.Tracks, "wave") {
			return
		}
		a.mu.Lock()
		a.batchID = batch.BatchID
		a.mu.Unlock()
		// refillWave срабатывает, когда следующего трека в очереди ещё не
		// было — prefetchNext на прошлом переходе нечего было качать. После
		// дозаливки батча он появился: подкачиваем его сразу, иначе первый
		// трек каждого батча оставался бы без префетча.
		a.prefetchNext()
		return
	}
	log.Printf("ротор: подкачка батча волны не удалась: %v", err)
	a.setWarning(refillWaveWarning)
	// Повтору передаём batchID на момент отказа: по нему горутина поймёт,
	// что волна перезапущена, пока шли паузы (см. retryRefillWave).
	a.mu.RLock()
	expectBatchID := a.batchID
	a.mu.RUnlock()
	a.retryRefillWave(c, last, expectBatchID)
}

// retryRefillWave — фоновые повторы подкачки батча после первой неудачи в
// refillWave. refillWaveBackoff задаёт жёсткий предел числа попыток — не
// бесконечный цикл: по его исчерпании предупреждение остаётся в
// State.Error до следующего успешного refillWave. Успех дозаливает батч
// в очередь и снимает именно это предупреждение (clearWarning).
//
// Перед дозаливкой повтор перепроверяет, что очередь всё ещё та самая
// волна: пока шли паузы, пользователь мог запустить плейлист (source
// сменился) или перезапустить волну (batchID сменился). Дозаливать
// устаревший батч в чужую очередь нельзя — в этом случае выходим молча:
// новая очередь сама о себе позаботится.
func (a *App) retryRefillWave(c *ymapi.Client, lastTrackID, expectBatchID string) {
	goSafe("ротор: повтор подкачки батча волны", func() error {
		var err error
		for _, pause := range refillWaveBackoff {
			<-time.After(pause)
			var batch *ymapi.WaveBatch
			batch, err = c.StationTracks(context.Background(), ymapi.WaveStationID, lastTrackID)
			if err == nil && len(batch.Tracks) == 0 {
				err = errors.New("ротор вернул пустой батч")
			}
			if err != nil {
				continue
			}
			// Перезапуск волны виден по сменившемуся batchID, уход в
			// плейлист — по отказу AppendIfSource: проверка источника и
			// дозаливка идут под одним захватом мьютекса очереди.
			a.mu.RLock()
			restarted := a.batchID != expectBatchID
			a.mu.RUnlock()
			if restarted {
				return nil
			}
			if !a.Queue.AppendIfSource(batch.Tracks, "wave") {
				return nil
			}
			a.mu.Lock()
			if a.batchID == expectBatchID {
				a.batchID = batch.BatchID
			}
			a.mu.Unlock()
			a.prefetchNext() // как в refillWave: появившийся следующий трек качаем сразу
			a.clearWarning(refillWaveWarning)
			return nil
		}
		return fmt.Errorf("не удалось подкачать батч волны после %d повторов: %w", len(refillWaveBackoff), err)
	})
}

// setWarning выставляет предупреждение в State.Error, не трогая status:
// воспроизведение оставшихся треков продолжается — это не полный отказ
// (в отличие от setStatus, который переключает оба поля разом).
func (a *App) setWarning(msg string) {
	a.mu.Lock()
	a.errText = msg
	a.mu.Unlock()
	a.publish()
}

// clearWarning снимает предупреждение, только если errText всё ещё равен
// именно тому тексту, который сама и выставила. Сравнение по точному
// значению, а не безусловный сброс в "": пока шли фоновые повторы, другой
// код мог успеть заменить errText на настоящую ошибку воспроизведения —
// затирать её результатом устаревшего повтора нельзя.
func (a *App) clearWarning(expect string) {
	a.mu.Lock()
	changed := a.errText == expect
	if changed {
		a.errText = ""
	}
	a.mu.Unlock()
	if changed {
		a.publish()
	}
}

// resetPosition обнуляет накопленную позицию воспроизведения. Обязана
// звонить в любом месте, где меняется текущий трек (handlePlay, handleNext,
// handlePrev, handleQueueIndex; находка 4) — иначе следующий вызов
// reportFinished унаследует позицию уже сыгранного трека и пришлёт её как
// "сколько проиграно" для трека, который ещё не начинал звучать.
func (a *App) resetPosition() {
	a.mu.Lock()
	a.position = 0
	a.mu.Unlock()
}

// reportTrackStarted уведомляет ротор о начале нового трека волны
// (ymapi.EventTrackStarted, находка 1). В отличие от reportFinished, это
// исключительно канал станции: применимо только когда источник очереди —
// "wave", событие не несёт "сколько проиграно" (см. RotorFeedback) и не
// затрагивает канал общей статистики (/play-audio). Источник проверяется
// внутри, так что вызывающему не нужно знать про "wave" — безопасно звать
// после ЛЮБОГО перехода на новый текущий трек.
func (a *App) reportTrackStarted() {
	_, _, source := a.Queue.Snapshot()
	if source != "wave" {
		return
	}
	cur := a.Queue.Current()
	if cur == nil {
		return
	}
	c, err := a.newClient()
	if err != nil {
		return
	}
	a.mu.RLock()
	batch := a.batchID
	a.mu.RUnlock()
	goSafe("ротор: trackStarted", func() error {
		return c.RotorFeedback(context.Background(), ymapi.WaveStationID, batch, ymapi.EventTrackStarted, cur.ID, 0)
	})
}

// reportFinished отправляет оба канала фидбека для трека, который только
// что перестал быть текущим (event — ymapi.EventSkip либо
// ymapi.EventTrackFinished, находка 1). Оба вызова асинхронны и идут через
// goSafe: фидбек ротора и статистика прослушиваний необязательны для
// воспроизведения и не должны ни прерывать его, ни ронять демон, если
// Яндекс перестанет их принимать. Зовётся ДО того, как очередь сдвинется
// на следующий трек — иначе cur и pos будут относиться уже к новому треку.
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

	// UID — идентификатор аккаунта из проверенного статуса (см.
	// PlayEvent.UID). a.Auth может быть nil (например, в тестах, которые
	// не проверяют авторизацию) — Auth.UID() безопасен на пустом Auth, но
	// не на нулевом указателе *Auth, поэтому проверка здесь обязательна.
	var uid int64
	if a.Auth != nil {
		uid = a.Auth.UID()
	}
	goSafe("статистика: playAudio", func() error {
		return c.PlayAudio(context.Background(), ymapi.PlayEvent{
			TrackID: cur.ID,
			// AlbumID оставлен пустым: player.Track не несёт числового
			// идентификатора альбома (только заголовок в Album) — заводить
			// под это поле пришлось бы менять контракт очереди и SSE-кадра
			// ради значения, которое ни разу не проверялось на боевом
			// сервисе (см. находку 9 брифа). Пустая строка — честное
			// "не задан", а не выдуманное значение.
			UID:           uid,
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
