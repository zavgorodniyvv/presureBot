package chart

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

// Визуальная проверка годового графика: CHART_YEAR_OUT=/tmp/year.png go test ./internal/chart
func TestRenderYearVisual(t *testing.T) {
	out := os.Getenv("CHART_YEAR_OUT")
	if out == "" {
		t.Skip()
	}
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	from := time.Date(2025, 10, 6, 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 365)
	var ms []pressure.Measurement
	for d := 0; d < 365; d++ {
		s := 130 + 8*math.Sin(float64(d)/20)
		ms = append(ms, pressure.Measurement{MeasuredAt: from.AddDate(0, 0, d).Add(8 * time.Hour), Systolic: int(s) + d%7, Diastolic: 82 + d%5})
	}
	img, err := Render(ms, pressure.Trend(ms, 72*time.Hour), Options{From: from, To: to, Location: loc, Title: "Год"})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(out, img, 0o644)
}
