package ymapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNullResultIsError проверяет, что явный "result":null не проходит как
// валидный пустой ответ. json.Unmarshal([]byte("null"), out) — задокументированный
// no-op: он не трогает out и не возвращает ошибку. Раньше это приводило к тому,
// что AccountStatus() возвращал пустую структуру и nil вместо ошибки — стартовая
// проверка демона делала неверный вывод («нет Плюса» вместо «API ответил не тем»).
func TestNullResultIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":null}`))
	}))
	defer srv.Close()

	c := NewWithBase("test-token", srv.URL)
	var out struct {
		Foo string `json:"foo"`
	}
	err := c.Get(context.Background(), "/whatever", nil, &out)
	if err == nil {
		t.Fatal("Get() = nil error, want error on explicit null result")
	}
}

// TestErrorBodyDoesNotLeakToken — регрессионный тест на требование "токен не
// покидает процесс демона". Сервер отвечает 500 с произвольным телом; текст
// ошибки, которую вернёт клиент, не должен содержать токен ни в каком виде
// (ни сырым, ни через заголовок Authorization).
func TestErrorBodyDoesNotLeakToken(t *testing.T) {
	const secretToken = "SUPER-SECRET-TOKEN-DO-NOT-LEAK-4711"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error: something broke"))
	}))
	defer srv.Close()

	c := NewWithBase(secretToken, srv.URL)
	_, err := c.AccountStatus(context.Background())
	if err == nil {
		t.Fatal("AccountStatus() = nil error, want error on 500")
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Fatalf("error text leaks token: %v", err)
	}
}

// TestHTTPClientHasNoOverallTimeout проверяет, что клиент, отдаваемый для
// потоковой загрузки (HTTPClient), не обрывает запрос по общему таймауту:
// http.Client.Timeout покрывает весь цикл запроса включая чтение тела, а
// трек может качаться дольше 20 секунд на слабой сети. Заодно проверяется,
// что транспорт — клон http.DefaultTransport: у него есть Proxy (обязательный
// корпоративный HTTP(S)_PROXY не должен молча ломать скачивание) и ограничена
// фаза получения заголовков ответа (зависший сервер не держит нас вечно).
func TestHTTPClientHasNoOverallTimeout(t *testing.T) {
	c := NewWithBase("t", "http://example.invalid")
	hc := c.HTTPClient()
	if got := hc.Timeout; got != 0 {
		t.Fatalf("HTTPClient().Timeout = %v, want 0 (no overall deadline for streaming)", got)
	}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("HTTPClient().Transport = %T, want *http.Transport", hc.Transport)
	}
	if tr.ResponseHeaderTimeout <= 0 {
		t.Fatalf("ResponseHeaderTimeout = %v, want > 0", tr.ResponseHeaderTimeout)
	}
	if tr.Proxy == nil {
		t.Fatal("Proxy = nil, want ProxyFromEnvironment из http.DefaultTransport")
	}
}
