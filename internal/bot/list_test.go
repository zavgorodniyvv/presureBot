package bot

import (
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

func TestFormatList(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	ms := []pressure.Measurement{
		{MeasuredAt: time.Date(2026, 10, 5, 17, 56, 0, 0, loc), Systolic: 151, Diastolic: 109},
		{MeasuredAt: time.Date(2026, 10, 5, 21, 10, 0, 0, loc), Systolic: 138, Diastolic: 92},
		{MeasuredAt: time.Date(2026, 10, 6, 5, 2, 0, 0, loc), Systolic: 98, Diastolic: 65},
	}
	got := formatList(ms, loc)
	want := "05.10 (пн)\n" +
		"    151/109   17:56\n" +
		"    138/92    21:10\n" +
		"06.10 (вт)\n" +
		"    98/65     05:02"
	if len(got) != 1 || got[0] != want {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n---\n"), want)
	}
}

func TestFormatListSplitsLongPeriods(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	start := time.Date(2025, 10, 6, 0, 0, 0, 0, loc)
	var ms []pressure.Measurement
	for d := 0; d < 365; d++ {
		for _, h := range []int{6, 21} {
			ms = append(ms, pressure.Measurement{MeasuredAt: start.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour), Systolic: 130, Diastolic: 85})
		}
	}
	chunks := formatList(ms, loc)
	if len(chunks) < 2 {
		t.Fatalf("год должен разбиться на несколько сообщений, получили %d", len(chunks))
	}
	lines := 0
	for _, c := range chunks {
		if len(c) > maxMessageLen {
			t.Errorf("часть длиной %d больше лимита", len(c))
		}
		if !strings.Contains(c[:12], "(") {
			t.Errorf("часть начинается не с даты: %q", c[:20])
		}
		lines += strings.Count(c, "\n") + 1
	}
	if lines != 365*3 {
		t.Errorf("строк %d, ожидали %d — что-то потерялось", lines, 365*3)
	}
}

func TestListButton(t *testing.T) {
	b, api, _ := newTestBot(0)

	b.Handle(photo("130/85/70", 0))
	b.Handle(text(btnFinish, 1))
	b.Handle(text(btnList, 2))
	b.Handle(tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID: "q", From: &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: chatID}}, Data: cbList + "7",
	}})
	got := api.lastText(t)
	// base — 05.10.2026 07:30 UTC = 10:30 по Израилю, понедельник.
	if !strings.Contains(got, "05.10 (пн)") || !strings.Contains(got, "130/85") || !strings.Contains(got, "10:30") {
		t.Errorf("список: %q", got)
	}
}
