package ymapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

const stationTracksFixture = `{"result":{"batchId":"batch-42","sequence":[
  {"track":{"id":"101","title":"Волна раз","available":true,"durationMs":90000,
   "artists":[{"name":"Икс"}],"albums":[{"title":"Игрек"}]}},
  {"track":{"id":"102","title":"Волна два","available":true,"durationMs":95000,
   "artists":[{"name":"Икс"}],"albums":[{"title":"Игрек"}]}}
]}}`

func TestStationTracksParsesBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rotor/station/user:onyourwave/tracks" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("queue"); got != "99" {
			t.Errorf("queue = %q, want 99", got)
		}
		w.Write([]byte(stationTracksFixture))
	}))
	defer srv.Close()

	got, err := NewWithBase("t", srv.URL).StationTracks(context.Background(), WaveStationID, "99")
	if err != nil {
		t.Fatalf("StationTracks: %v", err)
	}
	if got.BatchID != "batch-42" {
		t.Fatalf("BatchID = %q", got.BatchID)
	}
	if len(got.Tracks) != 2 || got.Tracks[0].ID != "101" {
		t.Fatalf("треки = %+v", got.Tracks)
	}
}

func TestRotorFeedbackSendsEvent(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/rotor/station/user:onyourwave/feedback" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("batch-id"); got != "batch-42" {
			t.Errorf("batch-id = %q", got)
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("разбор тела: %v", err)
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).RotorFeedback(
		context.Background(), WaveStationID, "batch-42", "trackFinished", "101", 90)
	if err != nil {
		t.Fatalf("RotorFeedback: %v", err)
	}
	if body["type"] != "trackFinished" {
		t.Fatalf("type = %v", body["type"])
	}
	if body["trackId"] != "101" {
		t.Fatalf("trackId = %v", body["trackId"])
	}
	if body["totalPlayedSeconds"] != float64(90) {
		t.Fatalf("totalPlayedSeconds = %v", body["totalPlayedSeconds"])
	}
}

// TestRotorFeedbackTrackStartedOmitsTotalPlayedSeconds: у события старта трека
// ещё нет «сколько проиграно» — лишнее поле ротор может принять за противоречие.
func TestRotorFeedbackTrackStartedOmitsTotalPlayedSeconds(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("разбор тела: %v", err)
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).RotorFeedback(
		context.Background(), WaveStationID, "batch-42", EventTrackStarted, "101", 0)
	if err != nil {
		t.Fatalf("RotorFeedback: %v", err)
	}
	if _, ok := body["totalPlayedSeconds"]; ok {
		t.Fatalf("totalPlayedSeconds не должно быть в теле trackStarted, тело = %v", body)
	}
	if body["trackId"] != "101" {
		t.Fatalf("trackId = %v", body["trackId"])
	}
}

// TestRotorFeedbackRadioStartedOmitsTrackID: задача 12 стартует волну именно так —
// с пустым trackID; ключ trackId не должен появляться в теле вовсе.
func TestRotorFeedbackRadioStartedOmitsTrackID(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("разбор тела: %v", err)
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).RotorFeedback(
		context.Background(), WaveStationID, "batch-42", EventRadioStarted, "", 0)
	if err != nil {
		t.Fatalf("RotorFeedback: %v", err)
	}
	if _, ok := body["trackId"]; ok {
		t.Fatalf("trackId не должно быть в теле radioStarted, тело = %v", body)
	}
	if body["type"] != "radioStarted" {
		t.Fatalf("type = %v", body["type"])
	}
}

func TestPlayAudioSendsForm(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/play-audio" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("разбор формы: %v", err)
		}
		form = r.PostForm
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).PlayAudio(context.Background(), PlayEvent{
		TrackID: "101", AlbumID: "9", From: "wave", PlayedSeconds: 88, TotalSeconds: 90,
	})
	if err != nil {
		t.Fatalf("PlayAudio: %v", err)
	}
	if v := form["track-id"]; len(v) == 0 || v[0] != "101" {
		t.Fatalf("track-id = %v", form["track-id"])
	}
	if v := form["total-played-seconds"]; len(v) == 0 || v[0] != "88.00" {
		t.Fatalf("total-played-seconds = %v", form["total-played-seconds"])
	}
	if v := form["from"]; len(v) == 0 || v[0] != "wave" {
		t.Fatalf("from = %v", form["from"])
	}
	// Нулевой UID — гипотеза первоисточника непроверена: не выдумываем значение,
	// отдаём пустую строку, а не "0".
	if v := form["uid"]; len(v) == 0 || v[0] != "" {
		t.Fatalf("uid = %v, want пустую строку при нулевом UID", form["uid"])
	}
	// PlayID не задан вызывающим — должен быть сгенерирован и не пуст.
	if v := form["play-id"]; len(v) == 0 || v[0] == "" {
		t.Fatalf("play-id = %v, ожидали непустое сгенерированное значение", form["play-id"])
	}
}

// TestPlayAudioUIDFormatted проверяет, что ненулевой UID уходит десятичной строкой.
func TestPlayAudioUIDFormatted(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("разбор формы: %v", err)
		}
		form = r.PostForm
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).PlayAudio(context.Background(), PlayEvent{
		TrackID: "101", AlbumID: "9", From: "wave", UID: 123456789,
	})
	if err != nil {
		t.Fatalf("PlayAudio: %v", err)
	}
	if v := form["uid"]; len(v) == 0 || v[0] != "123456789" {
		t.Fatalf("uid = %v, want 123456789", form["uid"])
	}
}

// TestPlayAudioPlayIDProvidedIsUsedAsIs: если вызывающий уже собрал play-id,
// PlayAudio не должен подменять его собственной генерацией.
func TestPlayAudioPlayIDProvidedIsUsedAsIs(t *testing.T) {
	var form map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("разбор формы: %v", err)
		}
		form = r.PostForm
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer srv.Close()

	err := NewWithBase("t", srv.URL).PlayAudio(context.Background(), PlayEvent{
		TrackID: "101", AlbumID: "9", From: "wave", PlayID: "custom-play-id",
	})
	if err != nil {
		t.Fatalf("PlayAudio: %v", err)
	}
	if v := form["play-id"]; len(v) == 0 || v[0] != "custom-play-id" {
		t.Fatalf("play-id = %v, want custom-play-id", form["play-id"])
	}
}

// TestNowUnixFloatSeam проверяет сам шов подмены времени: без него timestamp
// нигде не проверялся ни одним тестом пакета. Подмена — пакетная переменная,
// поэтому возвращаем прежнее значение через defer и не параллелим тест.
func TestNowUnixFloatSeam(t *testing.T) {
	prev := nowUnixFloat
	defer func() { nowUnixFloat = prev }()
	nowUnixFloat = func() float64 { return 1000000000.5 }

	var feedbackBody map[string]any
	feedbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("чтение тела: %v", err)
		}
		if err := json.Unmarshal(raw, &feedbackBody); err != nil {
			t.Errorf("разбор тела: %v", err)
		}
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer feedbackSrv.Close()

	if err := NewWithBase("t", feedbackSrv.URL).RotorFeedback(
		context.Background(), WaveStationID, "batch-42", EventTrackStarted, "101", 0); err != nil {
		t.Fatalf("RotorFeedback: %v", err)
	}
	if feedbackBody["timestamp"] != 1000000000.5 {
		t.Fatalf("timestamp = %v, want 1000000000.5", feedbackBody["timestamp"])
	}

	var form map[string][]string
	playSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("разбор формы: %v", err)
		}
		form = r.PostForm
		w.Write([]byte(`{"result":"ok"}`))
	}))
	defer playSrv.Close()

	prevISO := nowISO8601
	defer func() { nowISO8601 = prevISO }()
	nowISO8601 = func() string { return "2001-09-09T01:46:40.500Z" }

	if err := NewWithBase("t", playSrv.URL).PlayAudio(context.Background(), PlayEvent{
		TrackID: "101", AlbumID: "9", From: "wave",
	}); err != nil {
		t.Fatalf("PlayAudio: %v", err)
	}
	if v := form["timestamp"]; len(v) == 0 || v[0] != "2001-09-09T01:46:40.500Z" {
		t.Fatalf("timestamp = %v, want 2001-09-09T01:46:40.500Z", form["timestamp"])
	}
	if v := form["client-now"]; len(v) == 0 || v[0] != "2001-09-09T01:46:40.500Z" {
		t.Fatalf("client-now = %v, want 2001-09-09T01:46:40.500Z", form["client-now"])
	}
}
