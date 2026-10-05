package chart

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

func TestRender(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Jerusalem")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 26, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 10)

	var ms []pressure.Measurement
	for d := 0; d < 10; d++ {
		if d == 4 {
			continue // пропуск дня
		}
		ms = append(ms,
			pressure.Measurement{MeasuredAt: from.AddDate(0, 0, d).Add(8 * time.Hour), Systolic: 125 + d%3*7, Diastolic: 80 + d%4*3},
			pressure.Measurement{MeasuredAt: from.AddDate(0, 0, d).Add(21 * time.Hour), Systolic: 135 - d, Diastolic: 88 - d%2*5},
		)
	}
	trend := pressure.Trend(ms, 3*24*time.Hour)

	missed := []time.Time{from.AddDate(0, 0, 2), from.AddDate(0, 0, 7)}
	img, err := Render(ms, trend, Options{From: from, To: to, Location: loc, Title: "Давление за 10 дней", MissedPills: missed})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("не PNG: %v", err)
	}
	if cfg.Width < 1000 || cfg.Height < 500 {
		t.Errorf("слишком маленький график %dx%d", cfg.Width, cfg.Height)
	}

	// Для визуальной проверки: CHART_OUT=/tmp/chart.png go test ./internal/chart
	if out := os.Getenv("CHART_OUT"); out != "" {
		if err := os.WriteFile(out, img, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRenderEmpty(t *testing.T) {
	_, err := Render(nil, nil, Options{Location: time.UTC})
	if !errors.Is(err, ErrNoData) {
		t.Errorf("ожидали ErrNoData, получили %v", err)
	}
}

func TestDayStartsDST(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	// В 2026 году Израиль переходит на зимнее время 25 октября.
	from := time.Date(2026, 10, 24, 0, 0, 0, 0, loc)
	days := dayStarts(from, from.AddDate(0, 0, 3), loc)
	if len(days) != 4 {
		t.Fatalf("len = %d", len(days))
	}
	for _, d := range days {
		if d.Hour() != 0 || d.Minute() != 0 {
			t.Errorf("граница суток не в полночь: %v", d)
		}
	}
}
