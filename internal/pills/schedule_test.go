package pills

import (
	"testing"
	"time"
)

const userSchedule = "вс,вт,чт 04:30; пн,ср 06:30; пт,сб 07:00"

func TestParseSchedule(t *testing.T) {
	s, err := ParseSchedule(userSchedule)
	if err != nil {
		t.Fatal(err)
	}
	want := map[time.Weekday]string{
		time.Sunday: "04:30", time.Tuesday: "04:30", time.Thursday: "04:30",
		time.Monday: "06:30", time.Wednesday: "06:30",
		time.Friday: "07:00", time.Saturday: "07:00",
	}
	for wd, clock := range want {
		if got := formatClock(s[wd]); got != clock {
			t.Errorf("%v: %s, ожидали %s", wd, got, clock)
		}
	}
	if got := s.String(); got != "вт,чт,вс 04:30; пн,ср 06:30; пт,сб 07:00" {
		t.Errorf("String() = %q", got)
	}
	again, err := ParseSchedule(s.String())
	if err != nil || len(again) != 7 {
		t.Errorf("повторный разбор: %v, %v", again, err)
	}
}

func TestParseScheduleErrors(t *testing.T) {
	for _, bad := range []string{"пн", "пн 25:00", "xx 07:00", "пн 07:00; пн 08:00", "пн 7"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q: ожидали ошибку", bad)
		}
	}
	if s, err := ParseSchedule("  "); err != nil || len(s) != 0 {
		t.Errorf("пустое расписание: %v, %v", s, err)
	}
}

func TestWindowFor(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	s, _ := ParseSchedule(userSchedule)

	// 06.10.2026 — вторник: первое напоминание в 04:30.
	w, ok := s.WindowFor(time.Date(2026, 10, 6, 1, 0, 0, 0, loc), 12*time.Hour, loc)
	if !ok || w.Date != "2026-10-06" || w.Start.Format("15:04") != "04:30" || w.Cutoff.Format("15:04") != "12:00" {
		t.Errorf("вторник: %+v ok=%v", w, ok)
	}
	// Понедельник — 06:30.
	w, _ = s.WindowFor(time.Date(2026, 10, 5, 20, 0, 0, 0, loc), 12*time.Hour, loc)
	if w.Start.Format("15:04") != "06:30" {
		t.Errorf("понедельник: %v", w.Start)
	}
	// 25.10.2026 — воскресенье, день перехода на зимнее время: всё по местным часам.
	w, _ = s.WindowFor(time.Date(2026, 10, 25, 10, 0, 0, 0, loc), 12*time.Hour, loc)
	if w.Start.Format("2006-01-02 15:04") != "2026-10-25 04:30" {
		t.Errorf("день перевода часов: %v", w.Start)
	}
	// UTC-время, которое по Израилю уже следующий день.
	w, _ = s.WindowFor(time.Date(2026, 10, 5, 22, 30, 0, 0, time.UTC), 12*time.Hour, loc)
	if w.Date != "2026-10-06" {
		t.Errorf("дата по местному времени: %s", w.Date)
	}

	// Время напоминания позже срока — окно 5 часов.
	late, _ := ParseSchedule("пн 13:00")
	w, _ = late.WindowFor(time.Date(2026, 10, 5, 13, 0, 0, 0, loc), 12*time.Hour, loc)
	if w.Cutoff.Format("15:04") != "18:00" {
		t.Errorf("позднее напоминание: срок %v", w.Cutoff)
	}

	if _, ok := (Schedule{}).WindowFor(time.Now(), 12*time.Hour, loc); ok {
		t.Error("пустое расписание не должно давать окно")
	}
}
