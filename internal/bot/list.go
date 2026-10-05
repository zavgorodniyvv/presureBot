package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

// maxMessageLen — запас до лимита Telegram в 4096 символов на сообщение.
const maxMessageLen = 3800

var weekdayShort = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// sendList присылает измерения за период текстом, сгруппированными по дням.
func (b *Bot) sendList(chatID, userID int64, days int) {
	if days <= 0 || days > 3660 {
		days = 7
	}
	from, to := b.periodRange(days)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ms, err := b.store.List(ctx, userID, from, to)
	if err != nil {
		log.Printf("чтение измерений: %v", err)
		b.reply(chatID, "Не удалось прочитать данные из базы, попробуйте позже.")
		return
	}
	if len(ms) == 0 {
		b.reply(chatID, fmt.Sprintf("За период «%s» измерений нет.", strings.ToLower(periodName(days))))
		return
	}

	for _, chunk := range formatList(ms, b.cfg.Location) {
		msg := tgbotapi.NewMessage(chatID, "<pre>"+escape(chunk)+"</pre>")
		msg.ParseMode = tgbotapi.ModeHTML
		b.send(msg)
	}
}

// formatList группирует измерения по дням:
//
//	05.10 (пн)
//	    151/109   17:56
//
// и режет текст на части не длиннее maxMessageLen, не разрывая день.
func formatList(ms []pressure.Measurement, loc *time.Location) []string {
	var days []string
	var cur strings.Builder
	curDate := ""
	for _, m := range ms {
		t := m.MeasuredAt.In(loc)
		if date := t.Format("02.01.2006"); date != curDate {
			if cur.Len() > 0 {
				days = append(days, cur.String())
				cur.Reset()
			}
			curDate = date
			fmt.Fprintf(&cur, "%s (%s)\n", t.Format("02.01"), weekdayShort[t.Weekday()])
		}
		fmt.Fprintf(&cur, "    %-9s %s\n", fmt.Sprintf("%d/%d", m.Systolic, m.Diastolic), t.Format("15:04"))
	}
	days = append(days, cur.String())

	var chunks []string
	var chunk strings.Builder
	for _, d := range days {
		if chunk.Len() > 0 && chunk.Len()+len(d) > maxMessageLen {
			chunks = append(chunks, strings.TrimRight(chunk.String(), "\n"))
			chunk.Reset()
		}
		chunk.WriteString(d)
	}
	return append(chunks, strings.TrimRight(chunk.String(), "\n"))
}
