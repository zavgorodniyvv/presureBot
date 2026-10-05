// Package pressure содержит предметную логику: замеры давления, их проверку,
// усреднение серии замеров и расчёт линии тренда.
package pressure

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Reading — один замер с экрана тонометра.
type Reading struct {
	Systolic  int // верхнее давление
	Diastolic int // нижнее давление
	Pulse     int // 0, если пульс не распознан
	At        time.Time
	// Seq — порядок отправки (ID сообщения в Telegram). Нужен, когда несколько
	// фото пришли альбомом: у них одинаковое время с точностью до секунды.
	Seq int
}

func (r Reading) String() string {
	if r.Pulse > 0 {
		return fmt.Sprintf("%d/%d, пульс %d", r.Systolic, r.Diastolic, r.Pulse)
	}
	return fmt.Sprintf("%d/%d", r.Systolic, r.Diastolic)
}

// Validate отсекает явно неверно распознанные значения.
func (r Reading) Validate() error {
	switch {
	case r.Systolic < 60 || r.Systolic > 260:
		return fmt.Errorf("верхнее давление %d вне диапазона 60–260", r.Systolic)
	case r.Diastolic < 30 || r.Diastolic > 160:
		return fmt.Errorf("нижнее давление %d вне диапазона 30–160", r.Diastolic)
	case r.Systolic <= r.Diastolic:
		return fmt.Errorf("верхнее давление %d не больше нижнего %d", r.Systolic, r.Diastolic)
	case r.Pulse != 0 && (r.Pulse < 30 || r.Pulse > 220):
		return fmt.Errorf("пульс %d вне диапазона 30–220", r.Pulse)
	}
	return nil
}

// Measurement — итог серии замеров, именно он попадает в базу.
type Measurement struct {
	ID            string
	UserID        int64
	MeasuredAt    time.Time
	Systolic      int
	Diastolic     int
	Pulse         int // 0 — нет данных
	ReadingsCount int
}

func (m Measurement) String() string {
	return Reading{Systolic: m.Systolic, Diastolic: m.Diastolic, Pulse: m.Pulse}.String()
}

// Average сводит серию замеров к одному значению.
//
// Правило (так рекомендуют кардиологические общества, ESH/AHA): первый замер
// в серии обычно завышен из-за «реакции на манжету», поэтому при трёх и более
// замерах первый отбрасывается, а остальные усредняются. При одном-двух
// замерах берётся простое среднее. Время измерения — время первого замера.
// Пульс усредняется только по тем замерам, где он распознан.
func Average(readings []Reading) (Measurement, error) {
	if len(readings) == 0 {
		return Measurement{}, fmt.Errorf("нет замеров")
	}

	sorted := append([]Reading(nil), readings...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].At.Equal(sorted[j].At) {
			return sorted[i].At.Before(sorted[j].At)
		}
		return sorted[i].Seq < sorted[j].Seq
	})

	used := sorted
	if len(sorted) >= 3 {
		used = sorted[1:]
	}

	var sys, dia, pulse float64
	var pulseN int
	for _, r := range used {
		sys += float64(r.Systolic)
		dia += float64(r.Diastolic)
		if r.Pulse > 0 {
			pulse += float64(r.Pulse)
			pulseN++
		}
	}

	m := Measurement{
		MeasuredAt:    sorted[0].At,
		Systolic:      round(sys / float64(len(used))),
		Diastolic:     round(dia / float64(len(used))),
		ReadingsCount: len(readings),
	}
	if pulseN > 0 {
		m.Pulse = round(pulse / float64(pulseN))
	}
	return m, nil
}

func round(v float64) int { return int(math.Round(v)) }

// TrendPoint — значение линии тренда в момент очередного измерения.
type TrendPoint struct {
	At        time.Time
	Systolic  float64
	Diastolic float64
}

// Trend считает экспоненциально взвешенное среднее с учётом времени:
// вес прошлого измерения убывает вдвое каждые halfLife. Так тренд корректно
// работает при неравномерных замерах (два в один день, ни одного в другой).
// Среднее «скользящее назад»: в точке i учитываются только измерения до i
// включительно. measurements должны быть отсортированы по времени.
func Trend(measurements []Measurement, halfLife time.Duration) []TrendPoint {
	out := make([]TrendPoint, 0, len(measurements))
	for i, cur := range measurements {
		var wSum, sys, dia float64
		for _, m := range measurements[:i+1] {
			age := cur.MeasuredAt.Sub(m.MeasuredAt)
			w := math.Exp2(-float64(age) / float64(halfLife))
			wSum += w
			sys += w * float64(m.Systolic)
			dia += w * float64(m.Diastolic)
		}
		out = append(out, TrendPoint{At: cur.MeasuredAt, Systolic: sys / wSum, Diastolic: dia / wSum})
	}
	return out
}
