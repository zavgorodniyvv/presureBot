package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/access"
)

const (
	btnNewUser = "➕ Новый пользователь"

	cbAllow   = "allow:"   // запрос доступа: разрешить
	cbDeny    = "deny:"    // запрос доступа: отклонить
	cbDelUser = "deluser:" // удалить пользователя из списка

	// Повторный запрос доступа от того же человека — не чаще раза в сутки.
	requestInterval = 24 * time.Hour
)

const askUserText = "Пришлите Telegram ID пользователя числом или перешлите сюда любое его сообщение. Отмена — /cancel"

// accessRequest — незнакомец, о котором уже сообщили администратору.
type accessRequest struct {
	name string
	at   time.Time
}

// SetupCommands задаёт меню команд: общее для всех и расширенное — только в чате администратора.
func (b *Bot) SetupCommands() {
	common := []tgbotapi.BotCommand{
		{Command: "start", Description: "Справка и кнопки"},
		{Command: "chart", Description: "График давления"},
		{Command: "list", Description: "Список измерений"},
		{Command: "pills", Description: "Таблетки: расписание и пропуски"},
	}
	b.request(tgbotapi.NewSetMyCommands(common...))
	if admin := b.cfg.Access.Admin(); admin != 0 {
		cmds := append(common,
			tgbotapi.BotCommand{Command: "newuser", Description: "Добавить пользователя"},
			tgbotapi.BotCommand{Command: "users", Description: "Список пользователей"},
			tgbotapi.BotCommand{Command: "deluser", Description: "Удалить пользователя"},
		)
		b.request(tgbotapi.NewSetMyCommandsWithScope(tgbotapi.NewBotCommandScopeChat(admin), cmds...))
	}
}

func displayName(u *tgbotapi.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if u.UserName != "" {
		name = strings.TrimSpace(name + " @" + u.UserName)
	}
	return name
}

func userLabel(id int64, name string) string {
	if name == "" {
		return strconv.FormatInt(id, 10)
	}
	return fmt.Sprintf("%s (%d)", name, id)
}

// handleStranger — сообщение от того, кого нет в списке: сообщаем ему ID,
// а администратору присылаем запрос с кнопками.
func (b *Bot) handleStranger(m *tgbotapi.Message) {
	from := m.From
	name := displayName(from)
	log.Printf("доступ запрещён: user %d (@%s)", from.ID, from.UserName)

	text := fmt.Sprintf("Это личный бот. Ваш Telegram ID: %d", from.ID)
	admin := b.cfg.Access.Admin()
	if admin == 0 {
		b.reply(m.Chat.ID, text)
		return
	}

	now := b.now()
	b.mu.Lock()
	r, seen := b.requests[from.ID]
	fresh := !seen || now.Sub(r.at) >= requestInterval
	if fresh {
		b.requests[from.ID] = accessRequest{name: name, at: now}
	}
	b.mu.Unlock()

	if !fresh {
		b.reply(m.Chat.ID, text+"\nЗапрос на доступ уже отправлен владельцу.")
		return
	}
	b.reply(m.Chat.ID, text+"\nЗапрос на доступ отправлен владельцу.")

	msg := tgbotapi.NewMessage(admin, "Запрос доступа: "+userLabel(from.ID, name))
	id := strconv.FormatInt(from.ID, 10)
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Разрешить", cbAllow+id),
		tgbotapi.NewInlineKeyboardButtonData("✖️ Отклонить", cbDeny+id),
	))
	b.send(msg)
}

// handleAdmin обрабатывает команды администратора; false — сообщение не для него.
func (b *Bot) handleAdmin(m *tgbotapi.Message) bool {
	chatID := m.Chat.ID
	text := strings.TrimSpace(m.Text)
	cmd, arg, _ := strings.Cut(text, " ")
	arg = strings.TrimSpace(arg)

	if text == btnNewUser {
		cmd, arg = "/newuser", ""
	}

	switch {
	case cmd == "/newuser":
		if arg != "" {
			b.addUserByText(chatID, arg)
			return true
		}
		b.mu.Lock()
		b.awaitingUser = true
		b.mu.Unlock()
		b.reply(chatID, askUserText)
		return true
	case cmd == "/users":
		b.sendUsers(chatID)
		return true
	case cmd == "/deluser":
		if arg != "" {
			id, err := strconv.ParseInt(arg, 10, 64)
			if err != nil {
				b.reply(chatID, "ID — это число, например: /deluser 123456789")
				return true
			}
			b.removeUser(chatID, id)
			return true
		}
		b.askRemoveUser(chatID)
		return true
	}

	// Ждём ID после /newuser: число или пересланное сообщение. Любое другое
	// сообщение отменяет ожидание и обрабатывается как обычно.
	b.mu.Lock()
	waiting := b.awaitingUser
	b.awaitingUser = false
	b.mu.Unlock()
	if !waiting {
		return false
	}
	switch {
	case m.ForwardFrom != nil:
		b.addUser(chatID, m.ForwardFrom.ID, displayName(m.ForwardFrom))
	case m.ForwardSenderName != "":
		b.mu.Lock()
		b.awaitingUser = true
		b.mu.Unlock()
		b.reply(chatID, m.ForwardSenderName+" скрывает аккаунт в пересылках. Пришлите ID числом — бот сообщает его каждому, кто ему пишет.")
	case text == "/cancel":
		b.reply(chatID, "Добавление отменено.")
	default:
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return false
		}
		b.addUserByText(chatID, text)
	}
	return true
}

func (b *Bot) addUserByText(chatID int64, s string) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		b.reply(chatID, "ID — это положительное число, например: /newuser 123456789")
		return
	}
	b.mu.Lock()
	name := b.requests[id].name
	b.mu.Unlock()
	b.addUser(chatID, id, name)
}

// addUser добавляет пользователя в список и сообщает ему об этом.
func (b *Bot) addUser(chatID, id int64, name string) bool {
	if b.cfg.Access.IsAdmin(id) {
		b.reply(chatID, "Это вы — у администратора доступ есть всегда.")
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	added, err := b.cfg.Access.Add(ctx, access.User{ID: id, Name: name, AddedAt: b.now(), AddedBy: b.cfg.Access.Admin()})
	if err != nil {
		log.Printf("добавление пользователя %d: %v", id, err)
		b.reply(chatID, "Не удалось сохранить, попробуйте ещё раз.")
		return false
	}
	b.mu.Lock()
	delete(b.requests, id)
	b.mu.Unlock()
	if !added {
		b.reply(chatID, "Уже в списке: "+userLabel(id, name))
		return false
	}
	// Новым пользователям таблетки не напоминаем, пока они не зададут расписание.
	if err := b.disablePillsIfUnset(ctx, id); err != nil {
		log.Printf("расписание таблеток для %d: %v", id, err)
	}

	text := "✅ Добавлен: " + userLabel(id, name)
	if _, err := b.api.Send(b.withKeyboard(tgbotapi.NewMessage(id, "Вам открыли доступ к боту.\n\n"+helpText))); err != nil {
		log.Printf("уведомление пользователя %d: %v", id, err)
		text += "\nСообщить ему не получилось — пусть сам откроет бота и нажмёт /start."
	}
	b.reply(chatID, text)
	return true
}

func (b *Bot) disablePillsIfUnset(ctx context.Context, userID int64) error {
	_, ok, err := b.store.PillSchedule(ctx, userID)
	if err != nil || ok {
		return err
	}
	return b.store.SetPillSchedule(ctx, userID, "")
}

func (b *Bot) sendUsers(chatID int64) {
	users := b.cfg.Access.Users()
	if len(users) == 0 {
		b.reply(chatID, "Кроме вас, доступа ни у кого нет. Добавить — /newuser")
		return
	}
	var sb strings.Builder
	sb.WriteString("Пользователи:\n")
	for i, u := range users {
		fmt.Fprintf(&sb, "%d. %s", i+1, userLabel(u.ID, u.Name))
		if !u.AddedAt.IsZero() {
			sb.WriteString(", с " + u.AddedAt.In(b.cfg.Location).Format("02.01.2006"))
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nДобавить — /newuser, удалить — /deluser")
	b.reply(chatID, sb.String())
}

func (b *Bot) askRemoveUser(chatID int64) {
	users := b.cfg.Access.Users()
	if len(users) == 0 {
		b.reply(chatID, "Удалять некого: кроме вас, доступа ни у кого нет.")
		return
	}
	rows := make([][]tgbotapi.InlineKeyboardButton, len(users))
	for i, u := range users {
		rows[i] = tgbotapi.NewInlineKeyboardRow(tgbotapi.NewInlineKeyboardButtonData(
			"❌ "+userLabel(u.ID, u.Name), cbDelUser+strconv.FormatInt(u.ID, 10)))
	}
	msg := tgbotapi.NewMessage(chatID, "Кого удалить? Его измерения останутся в базе.")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(rows...)
	b.send(msg)
}

func (b *Bot) removeUser(chatID, id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	removed, err := b.cfg.Access.Remove(ctx, id)
	switch {
	case err != nil:
		log.Printf("удаление пользователя %d: %v", id, err)
		b.reply(chatID, "Не удалось удалить, попробуйте ещё раз.")
	case !removed:
		b.reply(chatID, fmt.Sprintf("Пользователя %d нет в списке.", id))
	case b.cfg.Access.FromEnv(id):
		b.reply(chatID, fmt.Sprintf("Удалён: %d. Но он есть в ALLOWED_USER_IDS — уберите его из .env, иначе после перезапуска доступ вернётся.", id))
	default:
		b.reply(chatID, fmt.Sprintf("Удалён: %d. Доступа к боту у него больше нет.", id))
	}
}

// handleAdminCallback — кнопки запроса доступа и удаления. Только для администратора.
func (b *Bot) handleAdminCallback(q *tgbotapi.CallbackQuery) {
	if !b.cfg.Access.IsAdmin(q.From.ID) {
		b.request(tgbotapi.NewCallback(q.ID, ""))
		return
	}
	prefix, idStr, _ := strings.Cut(q.Data, ":")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		b.request(tgbotapi.NewCallback(q.ID, ""))
		return
	}
	chatID := q.Message.Chat.ID
	b.request(tgbotapi.NewCallback(q.ID, ""))

	switch prefix + ":" {
	case cbAllow:
		b.mu.Lock()
		name := b.requests[id].name
		b.mu.Unlock()
		b.editPlain(chatID, q.Message.MessageID, q.Message.Text+"\n✅ Разрешено")
		b.addUser(chatID, id, name)
	case cbDeny:
		b.editPlain(chatID, q.Message.MessageID, q.Message.Text+"\n✖️ Отклонено")
	case cbDelUser:
		b.editPlain(chatID, q.Message.MessageID, "Удаление пользователя")
		b.removeUser(chatID, id)
	}
}
