package ymapi

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// saltMP3 — соль старой схемы подписи прямых ссылок.
const saltMP3 = "XGRlBW9FXlekgbPrRHuSiA"

// KeyFileInfo — ключ новой схемы (get-file-info).
// ВНИМАНИЕ: не подтверждён первичным источником, проверяется в Task 14.
const KeyFileInfo = "7tvSmFbyf5hJnIHhCimDDD"

// SignMP3 считает подпись прямой ссылки по старой схеме.
// path приходит из XML download-info и всегда начинается со слэша.
// В подпись входит path без первого символа — это часть протокола
// (отбрасывается именно первый символ, а не строго слэш). Пустой path
// не паникует: функция отбрасывает символ, только если он есть.
func SignMP3(path, s string) string {
	trimmed := path
	if len(trimmed) > 0 {
		trimmed = trimmed[1:]
	}
	sum := md5.Sum([]byte(saltMP3 + trimmed + s))
	return hex.EncodeToString(sum[:])
}

// DirectLinkMP3 собирает готовый URL для скачивания потока.
// Ссылка живёт около минуты, после чего отдаёт 410.
func DirectLinkMP3(host, path, ts, s string) string {
	return fmt.Sprintf("https://%s/get-mp3/%s/%s%s", host, SignMP3(path, s), ts, path)
}

// SignFileInfo считает подпись новой схемы: HMAC-SHA256 по конкатенации
// частей, base64 без padding.
func SignFileInfo(key string, parts ...string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(strings.Join(parts, "")))
	return strings.TrimRight(base64.StdEncoding.EncodeToString(mac.Sum(nil)), "=")
}
