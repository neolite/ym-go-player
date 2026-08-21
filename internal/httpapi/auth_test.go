package httpapi

import (
	"context"
	"encoding/json"
	"errors"
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
	getToken  string
	getErr    error
	setErr    error
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
	return s.setErr
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
// одном ответе, ни при каком сценарии.
func TestAuthNeverEchoesToken(t *testing.T) {
	const secret = "SUPERSECRET-DEADBEEF"
	_, mux := newTestAuth()

	var bodies []string

	setRec := httptest.NewRecorder()
	mux.ServeHTTP(setRec, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"`+secret+`"}`)))
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
		if strings.Contains(body, "SUPERSECRET") {
			t.Fatalf("ответ %d содержит токен: %s", i, body)
		}
	}
}

// Ошибка store.Set не должна протаскивать токен наружу через свой текст.
func TestSetTokenStoreFailureDoesNotLeakToken(t *testing.T) {
	const secret = "SUPERSECRET-STOREFAIL"
	store := &stubStore{setErr: errors.New("keychain: " + secret + " rejected")}
	a := NewAuth(store, okVerify)
	mux := http.NewServeMux()
	a.Register(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/auth/token",
		strings.NewReader(`{"token":"good"}`)))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("code = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("ответ содержит токен через ошибку хранилища: %s", rec.Body.String())
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
