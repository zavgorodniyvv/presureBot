// Package chart рисует график давления в PNG.
package chart

import (
	"bytes"
	"errors"
	"fmt"
	"image/color"
	"math"
	"time"

	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"

	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

// ErrNoData — в выбранном периоде нет ни одного измерения.
var ErrNoData = errors.New("нет измерений за период")

var (
	red       = color.RGBA{R: 0xd6, G: 0x27, B: 0x28, A: 0xff}
	blue      = color.RGBA{R: 0x1f, G: 0x5f, B: 0xd0, A: 0xff}
	redTrend  = color.NRGBA{R: 0xd6, G: 0x27, B: 0x28, A: 0x99}
	blueTrend = color.NRGBA{R: 0x1f, G: 0x5f, B: 0xd0, A: 0x99}
	dayLine   = color.Gray{Y: 0xb0}
	gridLine  = color.Gray{Y: 0xe6}
)

// Options — параметры графика.
type Options struct {
	From, To time.Time      // показываемый период [From, To); границы — полночь
	Location *time.Location // часовой пояс для дней и подписей
	Title    string
}

// Render рисует график.
//
// measurements — точки в периоде [From, To), отсортированные по времени.
// trend — линия тренда для тех же точек (см. pressure.Trend); её считают по
// более длинной истории, чтобы тренд в начале периода не «прыгал».
func Render(measurements []pressure.Measurement, trend []pressure.TrendPoint, opt Options) ([]byte, error) {
	if len(measurements) == 0 {
		return nil, ErrNoData
	}
	loc := opt.Location

	p := plot.New()
	p.Title.Text = opt.Title
	p.Title.TextStyle.Font.Size = vg.Points(16)
	p.Y.Label.Text = "мм рт. ст."
	p.Y.Label.TextStyle.Font.Size = vg.Points(13)
	p.X.Tick.Label.Font.Size = vg.Points(12)
	p.Y.Tick.Label.Font.Size = vg.Points(12)
	p.Legend.TextStyle.Font.Size = vg.Points(12)
	p.Legend.Top = true
	p.Legend.Left = true
	p.Legend.XOffs = vg.Points(10)

	sys := make(plotter.XYs, len(measurements))
	dia := make(plotter.XYs, len(measurements))
	yMin, yMax := math.Inf(1), math.Inf(-1)
	for i, m := range measurements {
		x := float64(m.MeasuredAt.Unix())
		sys[i] = plotter.XY{X: x, Y: float64(m.Systolic)}
		dia[i] = plotter.XY{X: x, Y: float64(m.Diastolic)}
		yMin = math.Min(yMin, float64(m.Diastolic))
		yMax = math.Max(yMax, float64(m.Systolic))
	}
	sysTrend := make(plotter.XYs, len(trend))
	diaTrend := make(plotter.XYs, len(trend))
	for i, tp := range trend {
		x := float64(tp.At.Unix())
		sysTrend[i] = plotter.XY{X: x, Y: tp.Systolic}
		diaTrend[i] = plotter.XY{X: x, Y: tp.Diastolic}
		yMin = math.Min(yMin, tp.Diastolic)
		yMax = math.Max(yMax, tp.Systolic)
	}

	// Шкала Y с запасом и кратная 10 — так проще читать значения.
	yMin = math.Floor((yMin-5)/10) * 10
	yMax = math.Ceil((yMax+5)/10) * 10
	p.Y.Min, p.Y.Max = yMin, yMax
	p.X.Min, p.X.Max = float64(opt.From.Unix()), float64(opt.To.Unix())

	grid := plotter.NewGrid()
	grid.Vertical.Color = nil
	grid.Horizontal.Color = gridLine
	p.Add(grid)

	// Тонкие вертикальные линии на границах дней. На длинных периодах дневных
	// линий слишком много (сливаются в серый фон) — делим по месяцам.
	periods, label := dayStarts(opt.From, opt.To, loc), dayLabel
	if len(periods) > maxDailyPeriods {
		periods, label = monthStarts(opt.From, opt.To, loc), monthLabel
	}
	for _, d := range periods[1:] {
		x := float64(d.Unix())
		l, err := plotter.NewLine(plotter.XYs{{X: x, Y: yMin}, {X: x, Y: yMax}})
		if err != nil {
			return nil, err
		}
		l.Color = dayLine
		l.Width = vg.Points(0.5)
		p.Add(l)
	}

	radius := vg.Points(3.5)
	if len(measurements) > 120 {
		radius = vg.Points(2)
	}
	if err := addSeries(p, sys, red, radius, "верхнее"); err != nil {
		return nil, err
	}
	if err := addSeries(p, dia, blue, radius, "нижнее"); err != nil {
		return nil, err
	}
	if err := addTrend(p, sysTrend, redTrend, "тренд верхнего"); err != nil {
		return nil, err
	}
	if err := addTrend(p, diaTrend, blueTrend, "тренд нижнего"); err != nil {
		return nil, err
	}

	p.X.Tick.Marker = periodTicker{starts: periods, label: label, loc: loc}
	p.Y.Tick.Marker = stepTicker{step: 10}

	width := vg.Length(1200) // точки; в PNG это 1600×800 px
	height := vg.Length(600)
	w, err := p.WriterTo(width, height, "png")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := w.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("рендер PNG: %w", err)
	}
	return buf.Bytes(), nil
}

func addSeries(p *plot.Plot, xys plotter.XYs, c color.Color, radius vg.Length, name string) error {
	lp, pts, err := plotter.NewLinePoints(xys)
	if err != nil {
		return err
	}
	lp.Color = c
	lp.Width = vg.Points(1.5)
	pts.Shape = draw.CircleGlyph{}
	pts.Color = c
	pts.Radius = radius
	p.Add(lp, pts)
	p.Legend.Add(name, lp, pts)
	return nil
}

func addTrend(p *plot.Plot, xys plotter.XYs, c color.Color, name string) error {
	if len(xys) == 0 {
		return nil
	}
	l, err := plotter.NewLine(xys)
	if err != nil {
		return err
	}
	l.Color = c
	l.Width = vg.Points(3)
	l.Dashes = []vg.Length{vg.Points(8), vg.Points(4)}
	p.Add(l)
	p.Legend.Add(name, l)
	return nil
}

// dayStarts возвращает начала всех суток в [from, to] по местному времени.
func dayStarts(from, to time.Time, loc *time.Location) []time.Time {
	f := from.In(loc)
	d := time.Date(f.Year(), f.Month(), f.Day(), 0, 0, 0, 0, loc)
	var out []time.Time
	for !d.After(to) {
		out = append(out, d)
		d = d.AddDate(0, 0, 1) // AddDate, а не +24h: учитывает переход на летнее время
	}
	return out
}

// maxDailyPeriods — до скольких дней на графике рисуются дневные разделители.
const maxDailyPeriods = 92

// monthStarts возвращает начала месяцев: первое — начало месяца, где лежит from.
func monthStarts(from, to time.Time, loc *time.Location) []time.Time {
	f := from.In(loc)
	m := time.Date(f.Year(), f.Month(), 1, 0, 0, 0, 0, loc)
	var out []time.Time
	for !m.After(to) {
		out = append(out, m)
		m = m.AddDate(0, 1, 0)
	}
	return out
}

var monthNames = [...]string{"янв", "фев", "мар", "апр", "май", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"}

func dayLabel(t time.Time) string   { return t.Format("02.01") }
func monthLabel(t time.Time) string { return monthNames[t.Month()-1] + t.Format(" 06") }

// periodTicker ставит подписи в середине каждого периода (суток или месяца),
// прореживая их, если периодов много.
type periodTicker struct {
	starts []time.Time
	label  func(time.Time) string
	loc    *time.Location
}

func (t periodTicker) Ticks(min, max float64) []plot.Tick {
	n := len(t.starts)
	step := 1
	for n/step > 16 {
		step++
	}
	var ticks []plot.Tick
	for i := 0; i+1 < n; i++ {
		if i%step != 0 {
			continue
		}
		mid := t.starts[i].Add(t.starts[i+1].Sub(t.starts[i]) / 2)
		x := float64(mid.Unix())
		if x < min || x > max {
			continue // месяц, начавшийся до начала графика, подписываем только если его середина видна
		}
		ticks = append(ticks, plot.Tick{Value: x, Label: t.label(t.starts[i].In(t.loc))})
	}
	return ticks
}

// stepTicker — подписи по оси Y с фиксированным шагом.
type stepTicker struct{ step float64 }

func (t stepTicker) Ticks(min, max float64) []plot.Tick {
	var ticks []plot.Tick
	for v := math.Ceil(min/t.step) * t.step; v <= max; v += t.step {
		ticks = append(ticks, plot.Tick{Value: v, Label: fmt.Sprintf("%.0f", v)})
	}
	return ticks
}
