package pressure

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)

func rd(sys, dia, pulse, minute int) Reading {
	return Reading{Systolic: sys, Diastolic: dia, Pulse: pulse, At: t0.Add(time.Duration(minute) * time.Minute)}
}

func TestAverage(t *testing.T) {
	tests := []struct {
		name     string
		readings []Reading
		want     Measurement
	}{
		{
			name:     "один замер",
			readings: []Reading{rd(130, 85, 70, 0)},
			want:     Measurement{MeasuredAt: t0, Systolic: 130, Diastolic: 85, Pulse: 70, ReadingsCount: 1},
		},
		{
			name:     "два замера — простое среднее",
			readings: []Reading{rd(130, 85, 70, 0), rd(125, 80, 66, 2)},
			want:     Measurement{MeasuredAt: t0, Systolic: 128, Diastolic: 83, Pulse: 68, ReadingsCount: 2},
		},
		{
			name:     "три замера — первый отбрасывается",
			readings: []Reading{rd(150, 95, 80, 0), rd(130, 84, 70, 2), rd(126, 80, 68, 4)},
			want:     Measurement{MeasuredAt: t0, Systolic: 128, Diastolic: 82, Pulse: 69, ReadingsCount: 3},
		},
		{
			name:     "порядок прихода не важен, первый — по времени",
			readings: []Reading{rd(126, 80, 68, 4), rd(150, 95, 80, 0), rd(130, 84, 70, 2)},
			want:     Measurement{MeasuredAt: t0, Systolic: 128, Diastolic: 82, Pulse: 69, ReadingsCount: 3},
		},
		{
			name: "альбом: одинаковое время, первый — по порядку отправки",
			readings: []Reading{
				{Systolic: 126, Diastolic: 80, Pulse: 68, At: t0, Seq: 12},
				{Systolic: 150, Diastolic: 95, Pulse: 80, At: t0, Seq: 10},
				{Systolic: 130, Diastolic: 84, Pulse: 70, At: t0, Seq: 11},
			},
			want: Measurement{MeasuredAt: t0, Systolic: 128, Diastolic: 82, Pulse: 69, ReadingsCount: 3},
		},
		{
			name:     "пульс только там, где распознан",
			readings: []Reading{rd(130, 85, 0, 0), rd(126, 81, 72, 2)},
			want:     Measurement{MeasuredAt: t0, Systolic: 128, Diastolic: 83, Pulse: 72, ReadingsCount: 2},
		},
		{
			name:     "пульс не распознан нигде",
			readings: []Reading{rd(130, 85, 0, 0)},
			want:     Measurement{MeasuredAt: t0, Systolic: 130, Diastolic: 85, ReadingsCount: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Average(tt.readings)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAverageEmpty(t *testing.T) {
	if _, err := Average(nil); err == nil {
		t.Fatal("ожидали ошибку для пустой серии")
	}
}

func TestValidate(t *testing.T) {
	ok := []Reading{rd(120, 80, 70, 0), rd(120, 80, 0, 0), rd(260, 160, 220, 0)}
	for _, r := range ok {
		if err := r.Validate(); err != nil {
			t.Errorf("%v: неожиданная ошибка %v", r, err)
		}
	}
	bad := []Reading{rd(80, 120, 70, 0), rd(300, 80, 70, 0), rd(120, 20, 70, 0), rd(120, 80, 10, 0), rd(90, 90, 0, 0)}
	for _, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("%v: ожидали ошибку", r)
		}
	}
}

func TestTrend(t *testing.T) {
	day := 24 * time.Hour
	ms := []Measurement{
		{MeasuredAt: t0, Systolic: 140, Diastolic: 90},
		{MeasuredAt: t0.Add(day), Systolic: 120, Diastolic: 80},
	}
	tr := Trend(ms, day)
	if len(tr) != 2 {
		t.Fatalf("len = %d", len(tr))
	}
	if tr[0].Systolic != 140 || tr[0].Diastolic != 90 {
		t.Errorf("первая точка тренда должна совпадать с измерением: %+v", tr[0])
	}
	// Через один период полураспада вес первого замера 0.5, текущего 1:
	// (0.5*140 + 120) / 1.5 = 126.67.
	if math.Abs(tr[1].Systolic-126.6667) > 0.001 {
		t.Errorf("systolic = %v", tr[1].Systolic)
	}
	if math.Abs(tr[1].Diastolic-83.3333) > 0.001 {
		t.Errorf("diastolic = %v", tr[1].Diastolic)
	}
}
