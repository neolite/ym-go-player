package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"

	"music212/internal/auth"
	"music212/internal/ymapi"
)

// AuthorizeURL — страница, где пользователь получает токен.
// После входа токен виден во фрагменте адресной строки; перехватить его
// программно нельзя (redirect_uri принадлежит Яндексу, фрагмент браузер на
// сервер не отправляет), поэтому пользователь копирует его вручную.
const AuthorizeURL = "https://oauth.yandex.ru/authorize?response_type=token&client_id=23cabbbdc6cd418abb4b39c32c41195d"

// AuthState — то, что видит фронтенд. Токена здесь нет и быть не должно.
type AuthState struct {
	Authorized bool   `json:"authorized"`
	Login      string `json:"login,omitempty"`
	HasPlus    bool   `json:"hasPlus"`
	Region     int    `json:"region,omitempty"`
	Message    string `json:"message,omitempty"`
	AuthURL    string `json:"authUrl,omitempty"`
}

// VerifyFunc проверяет токен и возвращает статус аккаунта.
type VerifyFunc func(ctx context.Context, token string) (*ymapi.AccountStatus, error)

// Auth обслуживает роуты авторизации и хранит проверенный статус.
type Auth struct {
	store  auth.Store
	verify VerifyFunc

	mu     sync.RWMutex
	status *ymapi.AccountStatus
}

// NewAuth собирает обработчик авторизации.
func NewAuth(store auth.Store, verify VerifyFunc) *Auth {
	return &Auth{store: store, verify: verify}
}

// DefaultVerify — боевая проверка токена через API.
func DefaultVerify(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
	return ymapi.New(token).AccountStatus(ctx)
}

// Register вешает роуты авторизации на роутер.
func (a *Auth) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/status", a.handleStatus)
	mux.HandleFunc("POST /api/auth/token", a.handleSetToken)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
}

// Token отдаёт сохранённый токен остальным обработчикам.
func (a *Auth) Token() (string, error) { return a.store.Get() }

// Status возвращает последний проверенный статус аккаунта. Результат может
// быть nil — до первой успешной проверки токена (например, сразу после
// запуска демона, когда токен в хранилище уже есть, но ещё не проверялся)
// и после logout. Вызывающий обязан проверить на nil перед разыменованием;
// для одного лишь идентификатора пользователя проще и безопаснее звать UID.
func (a *Auth) Status() *ymapi.AccountStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

// UID отдаёт идентификатор пользователя из последнего проверенного статуса.
// Возвращает 0, если статус ещё не получен: вызывающему не нужно
// разыменовывать возможный nil ради одного числа.
func (a *Auth) UID() int64 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.status == nil {
		return 0
	}
	return a.status.UID
}

func (a *Auth) handleStatus(w http.ResponseWriter, r *http.Request) {
	token, err := a.store.Get()
	if err != nil {
		if errors.Is(err, auth.ErrNoToken) {
			writeJSON(w, http.StatusOK, AuthState{
				Message: "Токен не задан. Откройте ссылку, войдите и вставьте токен из адресной строки.",
				AuthURL: AuthorizeURL,
			})
			return
		}
		// Хранилище (системная связка ключей) недоступно или сломано —
		// это не то же самое, что "токена нет", и совет вставить токен,
		// который уже вставлен, пользователю не поможет. Текст ошибки
		// хранилища в ответ не подставляем — только в лог демона.
		log.Printf("auth: не удалось прочитать токен из хранилища: %v", err)
		writeJSON(w, http.StatusOK, AuthState{
			Message: "Не удалось прочитать сохранённый токен из хранилища — проверьте доступ к связке ключей.",
		})
		return
	}
	st, err := a.verify(r.Context(), token)
	if err != nil {
		writeJSON(w, http.StatusOK, AuthState{
			Message: "Токен больше не работает — получите новый.",
			AuthURL: AuthorizeURL,
		})
		return
	}
	a.remember(st)
	writeJSON(w, http.StatusOK, stateFrom(st))
}

func (a *Auth) handleSetToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, AuthState{Message: "Не удалось прочитать запрос."})
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		writeJSON(w, http.StatusBadRequest, AuthState{Message: "Пустой токен."})
		return
	}

	st, err := a.verify(r.Context(), token)
	if err != nil {
		msg := "Токен не принят Яндексом."
		if errors.Is(err, ymapi.ErrForbidden) {
			msg = "Токен принят, но доступ запрещён — проверьте регион и подписку."
		}
		writeJSON(w, http.StatusUnauthorized, AuthState{Message: msg, AuthURL: AuthorizeURL})
		return
	}
	if err := a.store.Set(token); err != nil {
		// Текст ошибки хранилища может в теории содержать сам токен
		// (например, если реализация Store эхом отражает вход в сообщение
		// об ошибке) — в ответ он не подставляется ни в каком виде, только
		// в лог демона.
		log.Printf("auth: не удалось сохранить токен в хранилище: %v", err)
		writeJSON(w, http.StatusInternalServerError, AuthState{
			Message: "Не удалось сохранить токен в хранилище — проверьте доступ к связке ключей.",
		})
		return
	}
	a.remember(st)
	writeJSON(w, http.StatusOK, stateFrom(st))
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Delete(); err != nil {
		log.Printf("auth: не удалось удалить токен из хранилища: %v", err)
		writeJSON(w, http.StatusInternalServerError, AuthState{
			Message: "Не удалось удалить токен из хранилища — проверьте доступ к связке ключей.",
		})
		return
	}
	a.remember(nil)
	writeJSON(w, http.StatusOK, AuthState{Message: "Токен удалён.", AuthURL: AuthorizeURL})
}

func (a *Auth) remember(st *ymapi.AccountStatus) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = st
}

func stateFrom(st *ymapi.AccountStatus) AuthState {
	out := AuthState{
		Authorized: true,
		Login:      st.Login,
		HasPlus:    st.HasPlus,
		Region:     st.Region,
	}
	if !st.HasPlus {
		out.Message = "Подписка Плюс неактивна — воспроизведение будет недоступно."
	}
	return out
}

// writeJSON — единая точка сериализации ответов.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
