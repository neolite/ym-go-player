package ymapi

import "testing"

// TestIDString покрывает все ветки нормализации идентификатора: API отдаёт
// его то строкой, то числом (которое JSON-парсер в any превращает в
// float64), и мы должны получить предсказуемую строку в обоих случаях.
// Ветка неожиданного типа сейчас тихо возвращает "" — тест фиксирует именно
// это поведение (см. сомнение в отчёте, код не меняю).
func TestIDString(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"строка", "12345", "12345"},
		{"число (float64 как из json)", float64(67890), "67890"},
		{"неожиданный тип", true, ""},
		{"nil", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := idString(tc.in); got != tc.want {
				t.Errorf("idString(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestCoverURL покрывает подстановку размера в плейсхолдер обложки.
// Ветка "без плейсхолдера" сейчас тихо отдаёт адрес как есть (без замены и
// без ошибки), что на практике даст 404 у клиента — тест фиксирует текущее
// поведение (см. сомнение в отчёте, код не меняю).
func TestCoverURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"с плейсхолдером",
			"avatars.yandex.net/get-music-content/1/%%",
			"https://avatars.yandex.net/get-music-content/1/400x400",
		},
		{
			"без плейсхолдера",
			"avatars.yandex.net/get-music-content/1/orig",
			"https://avatars.yandex.net/get-music-content/1/orig",
		},
		{"пустой адрес", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := coverURL(tc.in); got != tc.want {
				t.Errorf("coverURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
