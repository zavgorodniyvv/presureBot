// Package recognize распознаёт показания тонометра на фотографии через OpenAI.
package recognize

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNotRecognized — модель не нашла на фото показаний тонометра.
var ErrNotRecognized = errors.New("показания не распознаны")

// Result — то, что удалось прочитать с экрана. Pulse = 0, если пульса нет.
type Result struct {
	Systolic  int
	Diastolic int
	Pulse     int
}

// Recognizer распознаёт фото экрана тонометра.
type Recognizer interface {
	Recognize(ctx context.Context, image []byte, mimeType string) (Result, error)
}

const prompt = `На фото — экран автоматического тонометра.
Прочитай показания: верхнее (систолическое, SYS) давление, нижнее (диастолическое, DIA) давление и пульс (PULSE, PUL, ударов в минуту).
Обычно они расположены сверху вниз в этом порядке. Не путай с датой, временем, номером ячейки памяти и средним давлением (MAP).
Цифры часто семисегментные — читай внимательно.
Если на фото нет экрана тонометра или цифры нельзя уверенно прочитать, верни ok=false.
Если пульса на экране нет, верни pulse=null.`

// OpenAI — реализация Recognizer через Chat Completions API.
type OpenAI struct {
	apiKey   string
	model    string
	endpoint string
	client   *http.Client
}

func NewOpenAI(apiKey, model string) *OpenAI {
	return &OpenAI{
		apiKey:   apiKey,
		model:    model,
		endpoint: "https://api.openai.com/v1/chat/completions",
		client:   &http.Client{Timeout: 60 * time.Second},
	}
}

// Строгая JSON-схема ответа: модель обязана вернуть ровно эти поля.
var responseFormat = map[string]any{
	"type": "json_schema",
	"json_schema": map[string]any{
		"name":   "blood_pressure",
		"strict": true,
		"schema": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"ok", "systolic", "diastolic", "pulse"},
			"properties": map[string]any{
				"ok":        map[string]any{"type": "boolean"},
				"systolic":  map[string]any{"type": []string{"integer", "null"}},
				"diastolic": map[string]any{"type": []string{"integer", "null"}},
				"pulse":     map[string]any{"type": []string{"integer", "null"}},
			},
		},
	},
}

type answer struct {
	OK        bool `json:"ok"`
	Systolic  *int `json:"systolic"`
	Diastolic *int `json:"diastolic"`
	Pulse     *int `json:"pulse"`
}

func (o *OpenAI) Recognize(ctx context.Context, image []byte, mimeType string) (Result, error) {
	dataURL := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(image)
	body, err := json.Marshal(map[string]any{
		"model":           o.model,
		"response_format": responseFormat,
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": prompt},
					map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURL, "detail": "high"}},
				},
			},
		},
	})
	if err != nil {
		return Result{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("запрос к OpenAI: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("чтение ответа OpenAI: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("OpenAI вернул %s: %s", resp.Status, truncate(raw, 500))
	}

	return parseResponse(raw)
}

func parseResponse(raw []byte) (Result, error) {
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Result{}, fmt.Errorf("разбор ответа OpenAI: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Result{}, fmt.Errorf("пустой ответ OpenAI: %s", truncate(raw, 500))
	}
	msg := resp.Choices[0].Message
	if msg.Refusal != "" {
		return Result{}, fmt.Errorf("%w: %s", ErrNotRecognized, msg.Refusal)
	}

	var a answer
	if err := json.Unmarshal([]byte(msg.Content), &a); err != nil {
		return Result{}, fmt.Errorf("разбор JSON показаний %q: %w", msg.Content, err)
	}
	if !a.OK || a.Systolic == nil || a.Diastolic == nil {
		return Result{}, ErrNotRecognized
	}

	r := Result{Systolic: *a.Systolic, Diastolic: *a.Diastolic}
	if a.Pulse != nil {
		r.Pulse = *a.Pulse
	}
	return r, nil
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
