package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/pills"
)

const (
	btnPills = "💊 Выпил таблетки"

	// Отметить приём за дату: pill:2006-01-02. Кнопка в напоминании — текст
	// сообщения заменяется отметкой; кнопка под итогом замера — отметка дописывается.
	cbPill      = "pill:"
	cbPillAfter = "pillm:"
)

// RunReminders каждые полминуты проверяет, не пора ли напомнить о таблетках.
// Состояние дня хранится в базе, поэтому после перезапуска бот продолжает
// с того же места: не повторяет финальное предупреждение и удаляет прошлое напоминание.
func (b *Bot) RunReminders(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		b.pillTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) pillTick(ctx context.Context) {
	now := b.now()
	for userID := range b.cfg.AllowedUsers {
		if err := b.remindUser(ctx, userID, now); err != nil {
			log.Printf("напоминание о таблетках для %d: %v", userID, err)
		}
	}
}

// pillSchedule — расписание пользователя из базы или общее по умолчанию.
func (b *Bot) pillSchedule(ctx context.Context, userID int64) (pills.Schedule, error) {
	s, ok, err := b.store.PillSchedule(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return b.cfg.PillSchedule, nil
	}
	return pills.ParseSchedule(s)
}

// pillWindow — окно напоминаний на сегодня; ok=false — сегодня таблеток нет.
func (b *Bot) pillWindow(ctx context.Context, userID int64, now time.Time) (pills.Window, bool, error) {
	sch, err := b.pillSchedule(ctx, userID)
	if err != nil {
		return pills.Window{}, false, err
	}
	w, ok := sch.WindowFor(now, b.cfg.PillCutoff, b.cfg.Location)
	return w, ok, nil
}

func (b *Bot) remindUser(ctx context.Context, userID int64, now time.Time) error {
	// Личный чат с пользователем имеет тот же ID, что и сам пользователь.
	chatID := userID

	b.pillMu.Lock()
	defer b.pillMu.Unlock()

	w, ok, err := b.pillWindow(ctx, userID, now)
	if err != nil || !ok || now.Before(w.Start) {
		return err
	}
	day, err := b.store.PillDay(ctx, userID, w.Date)
	if err != nil || day.Taken() {
		return err
	}

	if !now.Before(w.Cutoff) {
		if day.FinalSent {
			return nil
		}
		msg := tgbotapi.NewMessage(chatID, "❗ Таблетки сегодня не отмечены. Если всё-таки выпили — нажмите кнопку.")
		msg.ReplyMarkup = pillButton(w.Date)
		if _, err := b.api.Send(msg); err != nil {
			return err
		}
		return b.store.SetFinalSent(ctx, userID, w.Date)
	}

	if !day.LastReminderAt.IsZero() && now.Sub(day.LastReminderAt) < b.cfg.PillRepeat {
		return nil
	}

	// Каждое напоминание — новое сообщение (новое уведомление на телефоне),
	// а предыдущее удаляем, чтобы чат не зарастал.
	if day.LastReminderMsgID != 0 {
		b.request(tgbotapi.NewDeleteMessage(chatID, day.LastReminderMsgID))
	}
	text := "💊 Пора выпить таблетки!"
	if !day.LastReminderAt.IsZero() {
		text = fmt.Sprintf("💊 Таблетки! Напоминаю с %s.", w.Start.Format("15:04"))
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = pillButton(w.Date)
	sent, err := b.api.Send(msg)
	if err != nil {
		return err
	}
	return b.store.SaveReminder(ctx, userID, w.Date, now, sent.MessageID)
}

func pillButton(date string) tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Выпил", cbPill+date),
	))
}

// markPills отмечает приём за date и убирает кнопку с последнего напоминания
// (если это не skipMsgID — его вызывающий отредактирует сам).
// Возвращает время отметки и была ли она первой за этот день.
func (b *Bot) markPills(ctx context.Context, chatID, userID int64, date string, skipMsgID int) (time.Time, bool, error) {
	b.pillMu.Lock()
	defer b.pillMu.Unlock()

	day, first, err := b.store.MarkPillTaken(ctx, userID, date, b.now())
	if err != nil {
		return time.Time{}, false, err
	}
	if first && day.LastReminderMsgID != 0 && day.LastReminderMsgID != skipMsgID {
		b.editPlain(chatID, day.LastReminderMsgID, takenText(day.TakenAt, b.cfg.Location))
	}
	return day.TakenAt, first, nil
}

func takenText(at time.Time, loc *time.Location) string {
	return "✅ Таблетки выпиты в " + at.In(loc).Format("15:04")
}

// handlePillsButton — кнопка «Выпил таблетки» на клавиатуре: отметка за сегодня.
func (b *Bot) handlePillsButton(chatID, userID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	date := b.now().In(b.cfg.Location).Format(pills.DateLayout)
	at, first, err := b.markPills(ctx, chatID, userID, date, 0)
	if err != nil {
		log.Printf("отметка таблеток: %v", err)
		b.reply(chatID, "Не удалось записать отметку, попробуйте ещё раз.")
		return
	}
	if !first {
		b.reply(chatID, "Сегодня уже отмечено в "+at.In(b.cfg.Location).Format("15:04")+".")
		return
	}
	b.reply(chatID, takenText(at, b.cfg.Location))
}

func (b *Bot) handlePillCallback(q *tgbotapi.CallbackQuery) {
	afterMeasurement := strings.HasPrefix(q.Data, cbPillAfter)
	date := strings.TrimPrefix(strings.TrimPrefix(q.Data, cbPillAfter), cbPill)
	if _, err := time.Parse(pills.DateLayout, date); err != nil {
		b.request(tgbotapi.NewCallback(q.ID, ""))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	at, first, err := b.markPills(ctx, q.Message.Chat.ID, q.From.ID, date, q.Message.MessageID)
	if err != nil {
		log.Printf("отметка таблеток: %v", err)
		b.request(tgbotapi.NewCallback(q.ID, "Ошибка базы, попробуйте ещё раз"))
		return
	}
	answer := "Отмечено"
	if !first {
		answer = "Уже было отмечено"
	}
	b.request(tgbotapi.NewCallback(q.ID, answer))

	text := takenText(at, b.cfg.Location)
	if date != b.now().In(b.cfg.Location).Format(pills.DateLayout) {
		text += " (" + date[8:10] + "." + date[5:7] + ")"
	}
	if afterMeasurement {
		// Итог замера оставляем, вместо вопроса о таблетках — отметка.
		base, _, _ := strings.Cut(q.Message.Text, pillQuestion)
		text = strings.TrimSpace(base) + "\n\n" + text
	}
	b.editPlain(q.Message.Chat.ID, q.Message.MessageID, text)
}

func (b *Bot) editPlain(chatID int64, msgID int, text string) {
	b.send(tgbotapi.NewEditMessageText(chatID, msgID, text))
}

const pillQuestion = "💊 Таблетки сегодня ещё не отмечены."

// pillPrompt — напоминание под итогом утреннего замера, если таблетки ещё не отмечены.
func (b *Bot) pillPrompt(ctx context.Context, userID int64) (date string, ok bool) {
	now := b.now()
	w, has, err := b.pillWindow(ctx, userID, now)
	if err != nil || !has || !now.Before(w.Cutoff) {
		return "", false
	}
	day, err := b.store.PillDay(ctx, userID, w.Date)
	if err != nil || day.Taken() {
		return "", false
	}
	return w.Date, true
}

// handlePillsCommand: «/pills» — расписание и пропуски, «/pills <расписание>» — задать.
func (b *Bot) handlePillsCommand(chatID, userID int64, args string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if args = strings.TrimSpace(args); args != "" {
		if args == "-" {
			args = "" // выключить напоминания
		}
		sch, err := pills.ParseSchedule(args)
		if err != nil {
			b.reply(chatID, "Не понял расписание: "+err.Error()+"\n\nПример: /pills вс,вт,чт 04:30; пн,ср 06:30; пт,сб 07:00")
			return
		}
		if err := b.store.SetPillSchedule(ctx, userID, sch.String()); err != nil {
			log.Printf("сохранение расписания: %v", err)
			b.reply(chatID, "Не удалось сохранить расписание.")
			return
		}
		if len(sch) == 0 {
			b.reply(chatID, "Напоминания о таблетках выключены.")
			return
		}
		b.reply(chatID, "Расписание сохранено: "+sch.String())
		return
	}

	sch, err := b.pillSchedule(ctx, userID)
	if err != nil {
		log.Printf("чтение расписания: %v", err)
		b.reply(chatID, "Не удалось прочитать расписание.")
		return
	}
	var sb strings.Builder
	if len(sch) == 0 {
		sb.WriteString("Напоминания о таблетках выключены.\n")
	} else {
		fmt.Fprintf(&sb, "Напоминания: %s\nПовтор каждые %d мин до %s, пока не нажмёте «Выпил».\n",
			sch, int(b.cfg.PillRepeat.Minutes()), clock(b.cfg.PillCutoff))
	}
	sb.WriteString("Изменить: /pills вс,вт,чт 04:30; пн,ср 06:30; пт,сб 07:00\nВыключить: /pills -\n\n")

	missed, taken, err := b.missedDays(ctx, userID, 30)
	if err != nil {
		log.Printf("история таблеток: %v", err)
		b.reply(chatID, sb.String())
		return
	}
	fmt.Fprintf(&sb, "За 30 дней: отмечено %d", taken)
	if len(missed) == 0 {
		sb.WriteString(", пропусков нет 👍")
	} else {
		dates := make([]string, len(missed))
		for i, d := range missed {
			dates[i] = d.Format("02.01")
		}
		fmt.Fprintf(&sb, ", пропущено %d: %s", len(missed), strings.Join(dates, ", "))
	}
	b.reply(chatID, sb.String())
}

func clock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d.Hours()), int(d.Minutes())%60)
}

// missedDays — пропущенные дни (начала суток) за последние days дней и число отмеченных.
// Учитываются только дни, когда бот напоминал или была отметка, — дни до
// появления функции и дни простоя бота пропусками не считаются.
func (b *Bot) missedDays(ctx context.Context, userID int64, days int) ([]time.Time, int, error) {
	loc := b.cfg.Location
	now := b.now().In(loc)
	today := now.Format(pills.DateLayout)
	from := now.AddDate(0, 0, -days+1).Format(pills.DateLayout)
	return b.missedBetween(ctx, userID, from, today)
}

func (b *Bot) missedBetween(ctx context.Context, userID int64, from, to string) ([]time.Time, int, error) {
	loc := b.cfg.Location
	now := b.now()
	today := now.In(loc).Format(pills.DateLayout)

	list, err := b.store.PillDays(ctx, userID, from, to)
	if err != nil {
		return nil, 0, err
	}
	todayWindow, todayOK, err := b.pillWindow(ctx, userID, now)
	if err != nil {
		return nil, 0, err
	}

	var missed []time.Time
	taken := 0
	for _, d := range list {
		switch {
		case d.Taken():
			taken++
			continue
		case d.Date > today:
			continue
		case d.Date == today && (!todayOK || !todayWindow.Missed(d, now)):
			continue // сегодня срок ещё не прошёл
		}
		day, err := time.ParseInLocation(pills.DateLayout, d.Date, loc)
		if err == nil {
			missed = append(missed, day)
		}
	}
	return missed, taken, nil
}
