package recognize

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func completion(content string) string {
	b, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
	return string(b)
}

func TestParseResponse(t *testing.T) {
	got, err := parseResponse([]byte(completion(`{"ok":true,"systolic":132,"diastolic":84,"pulse":71}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got != (Result{Systolic: 132, Diastolic: 84, Pulse: 71}) {
		t.Errorf("got %+v", got)
	}

	got, err = parseResponse([]byte(completion(`{"ok":true,"systolic":132,"diastolic":84,"pulse":null}`)))
	if err != nil || got.Pulse != 0 {
		t.Errorf("без пульса: %+v, %v", got, err)
	}

	_, err = parseResponse([]byte(completion(`{"ok":false,"systolic":null,"diastolic":null,"pulse":null}`)))
	if !errors.Is(err, ErrNotRecognized) {
		t.Errorf("ожидали ErrNotRecognized, получили %v", err)
	}
}

func TestRecognizeRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("нет ключа в заголовке")
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req["model"] != "test-model" {
			t.Errorf("model = %v", req["model"])
		}
		if !strings.Contains(string(body), "data:image/jpeg;base64,AQID") {
			t.Errorf("в запросе нет картинки")
		}
		io.WriteString(w, completion(`{"ok":true,"systolic":120,"diastolic":80,"pulse":60}`))
	}))
	defer srv.Close()

	o := NewOpenAI("test-key", "test-model")
	o.endpoint = srv.URL
	got, err := o.Recognize(context.Background(), []byte{1, 2, 3}, "image/jpeg")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Result{Systolic: 120, Diastolic: 80, Pulse: 60}) {
		t.Errorf("got %+v", got)
	}
}

func TestRecognizeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	o := NewOpenAI("k", "m")
	o.endpoint = srv.URL
	if _, err := o.Recognize(context.Background(), []byte{1}, "image/jpeg"); err == nil || errors.Is(err, ErrNotRecognized) {
		t.Errorf("ожидали ошибку HTTP, получили %v", err)
	}
}
