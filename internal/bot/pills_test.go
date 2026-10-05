package bot

import (
	"context"
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/pills"
)

const testSchedule = "вс,вт,чт 04:30; пн,ср 06:30; пт,сб 07:00"

// pillBot — бот с расписанием пользователя и управляемыми часами.
func pillBot(t *testing.T) (*Bot, *fakeAPI, *fakeStore, *time.Time) {
	t.Helper()
	b, api, st := newTestBot(0)
	sch, err := pills.ParseSchedule(testSchedule)
	if err != nil {
		t.Fatal(err)
	}
	b.cfg.PillSchedule = sch
	now := new(time.Time)
	b.now = func() time.Time { return *now }
	return b, api, st, now
}

func at(loc *time.Location, day, hour, min int) time.Time {
	return time.Date(2026, 10, day, hour, min, 0, 0, loc)
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func pillCallback(data string, msgID int, text string) tgbotapi.Update {
	return tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "q",
		From:    &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{MessageID: msgID, Chat: &tgbotapi.Chat{ID: chatID}, Text: text},
		Data:    data,
	}}
}

func TestPillReminderFlow(t *testing.T) {
	b, api, st, now := pillBot(t)
	loc := b.cfg.Location
	ctx := context.Background()
	tick := func(tm time.Time) { *now = tm; b.pillTick(ctx) }

	// 06.10.2026 — вторник, напоминание в 04:30.
	tick(at(loc, 6, 4, 0))
	if api.count() != 0 {
		t.Fatal("напомнили раньше времени")
	}

	tick(at(loc, 6, 4, 30))
	if api.count() != 1 || !strings.Contains(api.lastText(t), "Пора выпить") {
		t.Fatalf("первое напоминание: %d сообщений, %q", api.count(), api.lastText(t))
	}
	first := st.pillDays["2026-10-06"].LastReminderMsgID

	tick(at(loc, 6, 4, 35))
	if api.count() != 1 {
		t.Fatal("повтор раньше чем через 10 минут")
	}

	tick(at(loc, 6, 4, 40))
	if api.count() != 2 {
		t.Fatal("нет повтора через 10 минут")
	}
	if len(api.deleted) != 1 || api.deleted[0] != first {
		t.Errorf("прошлое напоминание не удалено: %v", api.deleted)
	}

	// Перезапуск бота: состояние в базе, повторять раньше срока нельзя.
	b2, api2, _ := newTestBot(0)
	b2.store, b2.cfg, b2.now = st, b.cfg, b.now
	*now = at(loc, 6, 4, 45)
	b2.pillTick(ctx)
	if api2.count() != 0 {
		t.Fatal("после перезапуска напомнили раньше срока")
	}

	last := st.pillDays["2026-10-06"].LastReminderMsgID
	*now = at(loc, 6, 4, 42)
	b.Handle(pillCallback(cbPill+"2026-10-06", last, "💊 Таблетки!"))
	if !st.pillDays["2026-10-06"].Taken() {
		t.Fatal("приём не отмечен")
	}
	if got := api.lastText(t); got != "✅ Таблетки выпиты в 04:42" {
		t.Errorf("сообщение после отметки: %q", got)
	}

	n := api.count()
	tick(at(loc, 6, 5, 0))
	tick(at(loc, 6, 12, 30))
	if api.count() != n {
		t.Fatal("напоминания после отметки")
	}

	// 07.10 — среда, 06:30; не отмечаем — в 12:00 одно финальное предупреждение.
	tick(at(loc, 7, 6, 30))
	tick(at(loc, 7, 11, 55))
	n = api.count()
	tick(at(loc, 7, 12, 0))
	if api.count() != n+1 || !strings.Contains(api.lastText(t), "не отмечены") {
		t.Fatalf("финальное предупреждение: %q", api.lastText(t))
	}
	tick(at(loc, 7, 12, 30))
	tick(at(loc, 7, 18, 0))
	if api.count() != n+1 {
		t.Fatal("финальное предупреждение повторилось")
	}

	missed, taken, err := b.missedDays(ctx, userID, 30)
	if err != nil || taken != 1 || len(missed) != 1 || missed[0].Format("02.01") != "07.10" {
		t.Errorf("пропуски: %v taken=%d err=%v", missed, taken, err)
	}

	b.Handle(text("/pills", 0))
	if got := api.lastText(t); !strings.Contains(got, "пропущено 1: 07.10") || !strings.Contains(got, "вт,чт,вс 04:30") {
		t.Errorf("/pills: %q", got)
	}
}

func TestNoFinalWarningWithoutReminders(t *testing.T) {
	b, api, st, now := pillBot(t)
	loc := b.cfg.Location

	// Бот запущен вечером, утром он не работал: ни предупреждения, ни пропуска.
	*now = at(loc, 5, 19, 57)
	b.pillTick(context.Background())
	if api.count() != 0 {
		t.Fatalf("неожиданное сообщение: %q", api.lastText(t))
	}
	st.update("2026-10-04", func(d *pills.Day) { d.FinalSent = true }) // след старой версии
	missed, _, _ := b.missedDays(context.Background(), userID, 30)
	if len(missed) != 0 {
		t.Errorf("пропуски без напоминаний: %v", missed)
	}
}

func TestPillsButtonBeforeReminder(t *testing.T) {
	b, api, st, now := pillBot(t)
	loc := b.cfg.Location

	*now = at(loc, 9, 6, 15) // пятница, напоминание в 07:00
	b.Handle(text(btnPills, 0))
	if got := api.lastText(t); got != "✅ Таблетки выпиты в 06:15" {
		t.Errorf("ответ: %q", got)
	}
	b.Handle(text(btnPills, 0))
	if got := api.lastText(t); !strings.Contains(got, "уже отмечено") {
		t.Errorf("повторное нажатие: %q", got)
	}

	n := api.count()
	*now = at(loc, 9, 7, 0)
	b.pillTick(context.Background())
	if api.count() != n {
		t.Fatal("напомнили, хотя уже отмечено")
	}
	if !st.pillDays["2026-10-09"].Taken() {
		t.Fatal("нет отметки")
	}
}

func TestPillPromptAfterMeasurement(t *testing.T) {
	b, api, st, now := pillBot(t)
	loc := b.cfg.Location

	*now = at(loc, 8, 4, 50) // четверг, таблетки ещё не отмечены
	b.Handle(photo("130/85/70", 0))
	b.Handle(text(btnFinish, 1))

	api.mu.Lock()
	msg := api.sent[len(api.sent)-1].(tgbotapi.MessageConfig)
	api.mu.Unlock()
	if !strings.Contains(msg.Text, pillQuestion) {
		t.Fatalf("нет вопроса о таблетках: %q", msg.Text)
	}
	kb := msg.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	data := *kb.InlineKeyboard[1][0].CallbackData
	if data != cbPillAfter+"2026-10-08" {
		t.Fatalf("callback: %q", data)
	}

	b.Handle(pillCallback(data, 77, msg.Text))
	got := api.lastText(t)
	if !strings.Contains(got, "Записано: 130/85") || strings.Contains(got, pillQuestion) || !strings.Contains(got, "✅ Таблетки выпиты в 04:50") {
		t.Errorf("итог после отметки: %q", got)
	}
	if !st.pillDays["2026-10-08"].Taken() {
		t.Fatal("нет отметки")
	}

	// Если таблетки уже отмечены — вопроса нет.
	b.Handle(photo("128/84/70", 2))
	b.Handle(text(btnFinish, 3))
	if strings.Contains(api.lastText(t), pillQuestion) {
		t.Error("спросили о таблетках повторно")
	}
}

func TestPillsCommandSetsSchedule(t *testing.T) {
	b, api, st, now := pillBot(t)
	loc := b.cfg.Location

	b.Handle(text("/pills пн 08:00; xx 09:00", 0))
	if !strings.Contains(api.lastText(t), "Не понял") {
		t.Errorf("ошибка разбора: %q", api.lastText(t))
	}

	b.Handle(text("/pills пн,вт 08:15", 0))
	if st.schedules[userID] != "пн,вт 08:15" {
		t.Fatalf("сохранено: %q", st.schedules[userID])
	}
	// Вторник: теперь в 08:15, а не в 04:30.
	*now = at(loc, 6, 4, 30)
	b.pillTick(context.Background())
	if len(st.pillDays) != 0 {
		t.Fatal("напомнили по старому расписанию")
	}
	*now = at(loc, 6, 8, 15)
	b.pillTick(context.Background())
	if !strings.Contains(api.lastText(t), "Пора выпить") {
		t.Fatal("нет напоминания по новому расписанию")
	}

	b.Handle(text("/pills -", 0))
	if !strings.Contains(api.lastText(t), "выключены") {
		t.Errorf("выключение: %q", api.lastText(t))
	}
}
