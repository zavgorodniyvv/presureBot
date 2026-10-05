// Package pills — расписание утреннего приёма таблеток.
package pills

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DateLayout — формат дня приёма (по местному времени), ключ в базе.
const DateLayout = "2006-01-02"

// Schedule — время первого напоминания по дням недели. Нет ключа — в этот день не напоминаем.
type Schedule map[time.Weekday]time.Duration // смещение от полуночи

var dayNames = map[string]time.Weekday{
	"вс": time.Sunday, "пн": time.Monday, "вт": time.Tuesday, "ср": time.Wednesday,
	"чт": time.Thursday, "пт": time.Friday, "сб": time.Saturday,
}

// Порядок вывода — с понедельника.
var weekOrder = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}

func dayName(d time.Weekday) string {
	for name, wd := range dayNames {
		if wd == d {
			return name
		}
	}
	return "?"
}

// ParseSchedule разбирает строку вида «вс,вт,чт 04:30; пн,ср 06:30; пт,сб 07:00».
// Пустая строка — расписания нет.
func ParseSchedule(s string) (Schedule, error) {
	sch := Schedule{}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Fields(part)
		if len(fields) != 2 {
			return nil, fmt.Errorf("«%s»: ожидается «дни время», например «пн,ср 06:30»", part)
		}
		at, err := ParseClock(fields[1])
		if err != nil {
			return nil, fmt.Errorf("«%s»: %w", part, err)
		}
		for _, name := range strings.Split(fields[0], ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			wd, ok := dayNames[name]
			if !ok {
				return nil, fmt.Errorf("неизвестный день «%s», используйте пн вт ср чт пт сб вс", name)
			}
			if _, dup := sch[wd]; dup {
				return nil, fmt.Errorf("день «%s» указан дважды", name)
			}
			sch[wd] = at
		}
	}
	return sch, nil
}

// ParseClock разбирает время «7:00» / «07:00» в смещение от полуночи.
func ParseClock(s string) (time.Duration, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("неверное время «%s», нужно ЧЧ:ММ", s)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}

func formatClock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// String — обратное к ParseSchedule: дни с одинаковым временем объединяются.
func (s Schedule) String() string {
	byTime := map[time.Duration][]string{}
	var times []time.Duration
	for _, wd := range weekOrder {
		at, ok := s[wd]
		if !ok {
			continue
		}
		if _, seen := byTime[at]; !seen {
			times = append(times, at)
		}
		byTime[at] = append(byTime[at], dayName(wd))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	parts := make([]string, len(times))
	for i, at := range times {
		parts[i] = strings.Join(byTime[at], ",") + " " + formatClock(at)
	}
	return strings.Join(parts, "; ")
}

// Window — окно напоминаний на конкретный день.
type Window struct {
	Date   string    // день по местному времени, DateLayout
	Start  time.Time // первое напоминание
	Cutoff time.Time // после него — финальное предупреждение, повторы прекращаются
}

// WindowFor возвращает окно напоминаний на день, в котором находится now.
// ok=false — в этот день по расписанию напоминаний нет.
func (s Schedule) WindowFor(now time.Time, cutoff time.Duration, loc *time.Location) (Window, bool) {
	n := now.In(loc)
	at, ok := s[n.Weekday()]
	if !ok {
		return Window{}, false
	}
	w := Window{
		Date:   n.Format(DateLayout),
		Start:  atClock(n, at, loc),
		Cutoff: atClock(n, cutoff, loc),
	}
	// Напоминание назначено позже общего срока — даём ему 5 часов, но не дальше конца дня.
	if !w.Cutoff.After(w.Start) {
		endOfDay := time.Date(n.Year(), n.Month(), n.Day()+1, 0, 0, 0, 0, loc).Add(-time.Minute)
		w.Cutoff = w.Start.Add(5 * time.Hour)
		if w.Cutoff.After(endOfDay) {
			w.Cutoff = endOfDay
		}
	}
	return w, true
}

// Missed — день считается пропущенным, если приём не отмечен, а срок прошёл.
func (w Window) Missed(d Day, now time.Time) bool {
	return !d.Taken() && now.After(w.Cutoff)
}

// atClock — момент «день n, время d» по местным часам (корректно в дни перевода часов).
func atClock(n time.Time, d time.Duration, loc *time.Location) time.Time {
	return time.Date(n.Year(), n.Month(), n.Day(), int(d.Hours()), int(d.Minutes())%60, 0, 0, loc)
}

// Day — состояние приёма таблеток за один день. Документ создаётся при первом
// напоминании или отметке; дни без документа не считаются пропусками.
type Day struct {
	Date              string
	TakenAt           time.Time // нулевое — не отмечено
	LastReminderAt    time.Time
	LastReminderMsgID int
	FinalSent         bool
}

func (d Day) Taken() bool { return !d.TakenAt.IsZero() }
