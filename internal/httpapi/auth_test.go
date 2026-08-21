package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"music212/internal/auth"
	"music212/internal/ymapi"
)

func okVerify(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
	if token != "good" {
		return nil, ymapi.ErrUnauthorized
	}
	return &ymapi.AccountStatus{UID: 7, Login: "tester", Region: 225, HasPlus: true}, nil
}

func newTestAuth() (*Auth, *http.ServeMux) {
	a := NewAuth(auth.NewMemory(), okVerify)
	mux := http.NewServeMux()
	a.Register(mux)
	return a, mux
}

func decodeAuthState(t *testing.T, body *httptest.ResponseRecorder) AuthState {
	t.Helper()
	var st AuthState
	if err := json.NewDecoder(body.Body).Decode(&st); err != nil {
		t.Fatalf("разбор ответа: %v", err)
	}
	return st
}

func TestStatusUnauthorizedWithoutToken(t *testing.T) {
	_, mux := newTestAuth()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))

	st := decodeAuthState(t, rec)
	if st.Authorized {
		t.Fatal("без токена Authorized должен быть false")
	}
	if st.Message == "" {
		t.Fatal("ответ обязан объяснять, что делать дальше")
	}
}

func TestPostGoodTokenAuthorizes(t *testing.T) {
	_, mux := newTestAuth()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	st := decodeAuthState(t, rec)
	if !st.Authorized || st.Login != "tester" || !st.HasPlus {
		t.Fatalf("state = %+v", st)
	}
}

func TestPostBadTokenIsRejectedAndNotStored(t *testing.T) {
	a, mux := newTestAuth()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"bad"}`))
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	if _, err := a.Token(); !errors.Is(err, auth.ErrNoToken) {
		t.Fatal("невалидный токен не должен сохраняться")
	}
}

// Токен не должен утекать во фронтенд ни при каких обстоятельствах.
func TestStatusNeverLeaksToken(t *testing.T) {
	_, mux := newTestAuth()
	post := httptest.NewRecorder()
	mux.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`)))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))

	if strings.Contains(rec.Body.String(), "good") {
		t.Fatalf("ответ содержит токен: %s", rec.Body.String())
	}
}

func TestLogoutClearsToken(t *testing.T) {
	a, mux := newTestAuth()
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`)))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if _, err := a.Token(); !errors.Is(err, auth.ErrNoToken) {
		t.Fatal("после logout токен должен быть стёрт")
	}
}

// --- дополнение (task-11-addendum.md) ---

// stubStore — управляемая реализация auth.Store для тестов на нестандартные
// сбои хранилища (например, недоступную связку ключей), которые нельзя
// отличить от ErrNoToken при помощи auth.NewMemory().
type stubStore struct {
	getToken string
	getErr   error
	// setErr, если задан, строит ошибку по фактически переданному в Set
	// токену — это позволяет тестам моделировать хранилища, чей текст
	// ошибки отражает входное значение (как это, в принципе, может делать
	// go-keyring), а не подставлять независимую строку.
	setErr    func(token string) error
	deleteErr error
}

func (s *stubStore) Get() (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	return s.getToken, nil
}

func (s *stubStore) Set(token string) error {
	s.getToken = token
	if s.setErr == nil {
		return nil
	}
	return s.setErr(token)
}

func (s *stubStore) Delete() error {
	s.getToken = ""
	return s.deleteErr
}

// UID() не должен паниковать до первой проверки токена.
func TestUIDSafeWithoutStatus(t *testing.T) {
	a, _ := newTestAuth()
	if uid := a.UID(); uid != 0 {
		t.Fatalf("UID() = %d, свежий Auth должен отдавать 0", uid)
	}
}

func TestUIDReflectsVerifiedStatus(t *testing.T) {
	a, mux := newTestAuth()
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`)))

	if uid := a.UID(); uid != 7 {
		t.Fatalf("UID() = %d, want 7", uid)
	}

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	if uid := a.UID(); uid != 0 {
		t.Fatalf("после logout UID() = %d, want 0", uid)
	}
}

// GET /api/auth/status обязан различать "токена нет" и "хранилище сломано":
// в первом случае совет вставить токен уместен, во втором — нет.
func TestStatusDistinguishesNoTokenFromStoreFailure(t *testing.T) {
	noToken := NewAuth(auth.NewMemory(), okVerify)
	muxNoToken := http.NewServeMux()
	noToken.Register(muxNoToken)
	recNoToken := httptest.NewRecorder()
	muxNoToken.ServeHTTP(recNoToken, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	stNoToken := decodeAuthState(t, recNoToken)

	broken := NewAuth(&stubStore{getErr: errors.New("связка ключей заблокирована")}, okVerify)
	muxBroken := http.NewServeMux()
	broken.Register(muxBroken)
	recBroken := httptest.NewRecorder()
	muxBroken.ServeHTTP(recBroken, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	stBroken := decodeAuthState(t, recBroken)

	if stNoToken.Message == "" || stBroken.Message == "" {
		t.Fatal("оба ответа обязаны объяснять ситуацию")
	}
	if stNoToken.Message == stBroken.Message {
		t.Fatal("сообщение при отсутствии токена не должно совпадать с сообщением при отказе хранилища")
	}
	if strings.Contains(recBroken.Body.String(), "заблокирована") {
		t.Fatalf("текст ошибки хранилища не должен попадать в ответ: %s", recBroken.Body.String())
	}
}

// Центральное ограничение проекта: токен не покидает процесс демона ни в
// одном ответе, ни при каком сценарии — включая УСПЕШНУЮ авторизацию.
// verify намеренно принимает именно secret (а не статичный "good"), чтобы
// ветка сохранения/remember/stateFrom в handleSetToken реально исполнилась:
// проверка, которая никогда не проходит успешный путь, не проверяет ничего
// про самый вероятный канал утечки — эхо валидного токена во фронтенд.
func TestAuthNeverEchoesToken(t *testing.T) {
	const secret = "SUPERSECRET-DEADBEEF"
	verify := func(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
		if token != secret {
			return nil, ymapi.ErrUnauthorized
		}
		return &ymapi.AccountStatus{UID: 99, Login: "echoguard", Region: 225, HasPlus: true}, nil
	}
	a := NewAuth(auth.NewMemory(), verify)
	mux := http.NewServeMux()
	a.Register(mux)

	var bodies []string

	setRec := httptest.NewRecorder()
	mux.ServeHTTP(setRec, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"`+secret+`"}`)))
	if setRec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (успешная ветка обязана исполниться)", setRec.Code)
	}
	bodies = append(bodies, setRec.Body.String())

	statusRec := httptest.NewRecorder()
	mux.ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	bodies = append(bodies, statusRec.Body.String())

	badRec := httptest.NewRecorder()
	mux.ServeHTTP(badRec, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"`+secret+`-wrong"}`)))
	bodies = append(bodies, badRec.Body.String())

	logoutRec := httptest.NewRecorder()
	mux.ServeHTTP(logoutRec, httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil))
	bodies = append(bodies, logoutRec.Body.String())

	for i, body := range bodies {
		if strings.Contains(body, secret) {
			t.Fatalf("ответ %d содержит токен: %s", i, body)
		}
	}
}

// Ошибка store.Set не должна протаскивать токен наружу через свой текст —
// ни в ответе. Сама ошибка хранилища сконструирована из фактически
// переданного токена, как это может делать реальный keyring.Set.
func TestSetTokenStoreFailureDoesNotLeakToken(t *testing.T) {
	const secret = "SUPERSECRET-STOREFAIL"
	verify := func(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
		if token != secret {
			return nil, ymapi.ErrUnauthorized
		}
		return &ymapi.AccountStatus{UID: 5, Login: "storefail", Region: 225, HasPlus: true}, nil
	}
	store := &stubStore{setErr: func(token string) error {
		return fmt.Errorf("keychain: %s rejected", token)
	}}
	a := NewAuth(store, verify)
	mux := http.NewServeMux()
	a.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"`+secret+`"}`)))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("ответ содержит токен через ошибку хранилища: %s", rec.Body.String())
	}
}

// То же самое, но проверяется лог демона, а не тело ответа: именно этот
// канал утечки не проверял ни один тест раньше, и именно в нём токен
// утекал на текущем (до правки) коде. log.SetOutput меняет глобальное
// состояние пакета log — приёмник восстанавливается через defer, тест не
// помечен t.Parallel(), чтобы подмена не протекла в соседние тесты пакета.
func TestSetTokenStoreFailureDoesNotLeakTokenToLog(t *testing.T) {
	const secret = "SUPERSECRET-LOGLEAK"
	verify := func(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
		if token != secret {
			return nil, ymapi.ErrUnauthorized
		}
		return &ymapi.AccountStatus{UID: 6, Login: "logleak", Region: 225, HasPlus: true}, nil
	}
	store := &stubStore{setErr: func(token string) error {
		return fmt.Errorf("keychain: %s rejected", token)
	}}
	a := NewAuth(store, verify)
	mux := http.NewServeMux()
	a.Register(mux)

	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"`+secret+`"}`)))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
	if strings.Contains(logBuf.String(), secret) {
		t.Fatalf("лог демона содержит токен: %s", logBuf.String())
	}
}

func TestPostTokenRejectsMalformedRequests(t *testing.T) {
	a, mux := newTestAuth()

	cases := []struct {
		name string
		body string
	}{
		{"пустое тело", ""},
		{"пустой токен", `{"token":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(c.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400", rec.Code)
			}
			st := decodeAuthState(t, rec)
			if st.Message == "" {
				t.Fatal("сообщение об ошибке не должно быть пустым")
			}
		})
	}
	if _, err := a.Token(); !errors.Is(err, auth.ErrNoToken) {
		t.Fatal("некорректные запросы не должны сохранять токен")
	}
}

func TestPostTokenTooLargeBodyRejected(t *testing.T) {
	_, mux := newTestAuth()
	huge := `{"token":"` + strings.Repeat("a", 9<<10) + `"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(huge)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
}

func TestPostTokenForbiddenGetsSpecialMessage(t *testing.T) {
	forbiddenVerify := func(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
		return nil, ymapi.ErrForbidden
	}
	a := NewAuth(auth.NewMemory(), forbiddenVerify)
	mux := http.NewServeMux()
	a.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"any"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rec.Code)
	}
	st := decodeAuthState(t, rec)
	if !strings.Contains(st.Message, "регион") && !strings.Contains(st.Message, "одписк") {
		t.Fatalf("сообщение не объясняет причину отказа: %q", st.Message)
	}
}

func TestPostTokenWithoutPlusStillAuthorizes(t *testing.T) {
	noPlusVerify := func(ctx context.Context, token string) (*ymapi.AccountStatus, error) {
		return &ymapi.AccountStatus{UID: 1, Login: "noplus", Region: 225, HasPlus: false}, nil
	}
	a := NewAuth(auth.NewMemory(), noPlusVerify)
	mux := http.NewServeMux()
	a.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"any"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	st := decodeAuthState(t, rec)
	if !st.Authorized {
		t.Fatal("аккаунт без Плюса всё равно должен считаться авторизованным: библиотека видна")
	}
	if st.HasPlus {
		t.Fatal("HasPlus должен быть false")
	}
	if st.Message == "" {
		t.Fatal("должно быть сообщение о неактивной подписке")
	}
}

func TestResponsesHaveJSONContentType(t *testing.T) {
	_, mux := newTestAuth()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/status", nil))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want application/json; charset=utf-8", ct)
	}
}

// Прямой юнит-тест redactToken: раньше эта функция была покрыта только
// косвенно, через HTTP-обработчики, а ветка "token == \"\"" через
// handleSetToken вообще недостижима — пустой токен отсекается раньше, на
// входе. Без прямого теста корректность этой ветки была выводом из чтения
// кода, а не фактом, подтверждённым прогоном.
func TestRedactToken(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		token string
		want  string
	}{
		{
			// Пустой token — защита от ловушки strings.ReplaceAll(s, "", x),
			// которая иначе вставила бы замену между каждой парой символов
			// строки. Текст ошибки должен вернуться без изменений.
			name:  "пустой токен не портит строку",
			err:   errors.New("keychain: связка ключей недоступна"),
			token: "",
			want:  "keychain: связка ключей недоступна",
		},
		{
			name:  "токен в тексте вымаран",
			err:   errors.New("keychain: SECRET-ABC123 rejected"),
			token: "SECRET-ABC123",
			want:  "keychain: <токен вымаран> rejected",
		},
		{
			name:  "текст без токена не испорчен",
			err:   errors.New("keychain: связка ключей заблокирована"),
			token: "SECRET-ABC123",
			want:  "keychain: связка ключей заблокирована",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactToken(c.err, c.token)
			if got != c.want {
				t.Fatalf("redactToken() = %q, want %q", got, c.want)
			}
			if c.token != "" && strings.Contains(got, c.token) {
				t.Fatalf("redactToken() всё ещё содержит токен: %q", got)
			}
		})
	}
}
