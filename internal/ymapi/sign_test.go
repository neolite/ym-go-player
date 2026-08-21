package ymapi

import "testing"

// Вектор посчитан независимо: md5("XGRlBW9FXlekgbPrRHuSiA" + "abc/def.mp3" + "somesalt").
func TestSignMP3(t *testing.T) {
	got := SignMP3("/abc/def.mp3", "somesalt")
	const want = "cb7ecaa6654cd1134af397cdbeb178e2"
	if got != want {
		t.Fatalf("SignMP3 = %q, want %q", got, want)
	}
}

// Первый символ path обязан отбрасываться — это часть протокола, а не косметика.
func TestSignMP3DropsLeadingSlash(t *testing.T) {
	withSlash := SignMP3("/abc/def.mp3", "somesalt")
	if withSlash == SignMP3("abc/def.mp3", "somesalt") {
		t.Fatal("подпись не должна совпадать: ведущий слэш обязан отбрасываться")
	}
}

func TestDirectLinkMP3(t *testing.T) {
	got := DirectLinkMP3("s1.example.net", "/abc/def.mp3", "1700000000", "somesalt")
	const want = "https://s1.example.net/get-mp3/cb7ecaa6654cd1134af397cdbeb178e2/1700000000/abc/def.mp3"
	if got != want {
		t.Fatalf("DirectLinkMP3 = %q, want %q", got, want)
	}
}

// Вектор посчитан независимо: hmac-sha256 ключом KeyFileInfo ("7tvSmFbyf5hJnIHhCimDDD")
// по конкатенации "1700000000"+"12345"+"lossless"+"flac"+"raw", base64 без padding.
func TestSignFileInfo(t *testing.T) {
	got := SignFileInfo(KeyFileInfo, "1700000000", "12345", "lossless", "flac", "raw")
	const want = "DevZeCG/M+0jdeH6+xBnQ4+IBNXCimprqilmw1mnEw8"
	if got != want {
		t.Fatalf("SignFileInfo = %q, want %q", got, want)
	}
}

func TestSignFileInfoHasNoPadding(t *testing.T) {
	got := SignFileInfo("k", "a")
	for _, c := range got {
		if c == '=' {
			t.Fatalf("подпись содержит padding: %q", got)
		}
	}
}
