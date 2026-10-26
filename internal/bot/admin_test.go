package bot

import (
	"strings"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const strangerID = int64(555)

// textsTo — тексты сообщений, отправленных в чат.
func (f *fakeAPI) textsTo(chat int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.sent {
		if m, ok := c.(tgbotapi.MessageConfig); ok && m.ChatID == chat {
			out = append(out, m.Text)
		}
	}
	return out
}

func (f *fakeAPI) lastTo(t *testing.T, chat int64) tgbotapi.MessageConfig {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.sent) - 1; i >= 0; i-- {
		if m, ok := f.sent[i].(tgbotapi.MessageConfig); ok && m.ChatID == chat {
			return m
		}
	}
	t.Fatalf("в чат %d ничего не отправлено", chat)
	return tgbotapi.MessageConfig{}
}

func from(id int64, s string) tgbotapi.Update {
	u := text(s, 0)
	u.Message.From = &tgbotapi.User{ID: id, FirstName: "Аня", UserName: "anya"}
	u.Message.Chat = &tgbotapi.Chat{ID: id}
	return u
}

func adminCallback(data string) tgbotapi.Update {
	return tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "q",
		From:    &tgbotapi.User{ID: adminID},
		Message: &tgbotapi.Message{MessageID: 42, Chat: &tgbotapi.Chat{ID: adminID}, Text: "Запрос доступа"},
		Data:    data,
	}}
}

func TestAccessRequestAllow(t *testing.T) {
	b, api, st := newTestBot(0)

	b.Handle(from(strangerID, "привет"))
	if got := api.lastTo(t, strangerID).Text; !strings.Contains(got, "555") || !strings.Contains(got, "отправлен владельцу") {
		t.Errorf("ответ незнакомцу: %q", got)
	}
	req := api.lastTo(t, adminID)
	if !strings.Contains(req.Text, "Аня @anya (555)") {
		t.Errorf("запрос админу: %q", req.Text)
	}
	kb := req.ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	allow := *kb.InlineKeyboard[0][0].CallbackData

	// Повторное сообщение в течение суток — без нового запроса.
	b.Handle(from(strangerID, "ну пусти"))
	if n := len(api.textsTo(adminID)); n != 1 {
		t.Errorf("запросов админу: %d", n)
	}

	// Чужое нажатие «Разрешить» ничего не даёт.
	cb := adminCallback(allow)
	cb.CallbackQuery.From.ID = userID
	b.Handle(cb)
	if b.cfg.Access.Allowed(strangerID) {
		t.Fatal("доступ выдал не админ")
	}

	b.Handle(adminCallback(allow))
	if !b.cfg.Access.Allowed(strangerID) {
		t.Fatal("доступ не выдан")
	}
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "Добавлен: Аня @anya (555)") {
		t.Errorf("ответ админу: %q", got)
	}
	if got := api.lastTo(t, strangerID).Text; !strings.Contains(got, "открыли доступ") {
		t.Errorf("уведомление: %q", got)
	}
	if v, ok := st.schedules[strangerID]; !ok || v != "" {
		t.Errorf("таблетки новому пользователю: %q ok=%v", v, ok)
	}

	// Теперь сообщения обрабатываются как обычно.
	b.Handle(from(strangerID, "/start"))
	if got := api.lastTo(t, strangerID).Text; got != helpText {
		t.Errorf("после доступа: %q", got)
	}
}

func TestAccessRequestRepeatsAfterDay(t *testing.T) {
	b, api, _ := newTestBot(0)
	now := base
	b.now = func() time.Time { return now }

	b.Handle(from(strangerID, "привет"))
	b.Handle(adminCallback(cbDeny + "555"))
	if b.cfg.Access.Allowed(strangerID) {
		t.Fatal("отклонённый получил доступ")
	}
	now = now.Add(25 * time.Hour)
	b.Handle(from(strangerID, "привет"))
	if n := len(api.textsTo(adminID)); n != 2 {
		t.Errorf("запросов админу через сутки: %d", n)
	}
}

func TestNewUserByID(t *testing.T) {
	b, api, _ := newTestBot(0)

	b.Handle(from(adminID, btnNewUser))
	if got := api.lastTo(t, adminID).Text; got != askUserText {
		t.Errorf("вопрос: %q", got)
	}
	b.Handle(from(adminID, "12x"))
	if b.cfg.Access.Allowed(12) {
		t.Fatal("добавлен по мусору")
	}
	// Мусор отменил ожидание — следующее число уже не ID пользователя.
	b.Handle(from(adminID, "777"))
	if b.cfg.Access.Allowed(777) {
		t.Fatal("ожидание не отменилось")
	}

	b.Handle(from(adminID, "/newuser"))
	b.Handle(from(adminID, "777"))
	if !b.cfg.Access.Allowed(777) {
		t.Fatal("не добавлен")
	}
	defer func() {
		b.Handle(from(adminID, "/deluser 777"))
		if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "больше нет") {
			t.Errorf("удаление добавленного: %q", got)
		}
	}()
	b.Handle(from(adminID, "/newuser 777"))
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "Уже в списке") {
		t.Errorf("повтор: %q", got)
	}
	b.Handle(from(adminID, "/newuser 1"))
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "администратора") {
		t.Errorf("себя: %q", got)
	}

	// Имя запоминается, когда пользователь напишет сам.
	b.Handle(from(777, "/start"))
	b.Handle(from(adminID, "/users"))
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "Аня @anya (777)") {
		t.Errorf("список: %q", got)
	}
}

func TestNewUserByForward(t *testing.T) {
	b, api, _ := newTestBot(0)

	b.Handle(from(adminID, "/newuser"))
	u := from(adminID, "сообщение Бориса")
	u.Message.ForwardSenderName = "Борис"
	b.Handle(u)
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "скрывает аккаунт") {
		t.Errorf("скрытый аккаунт: %q", got)
	}
	u = from(adminID, "сообщение Бориса")
	u.Message.ForwardFrom = &tgbotapi.User{ID: 888, FirstName: "Борис"}
	b.Handle(u)
	if !b.cfg.Access.Allowed(888) {
		t.Fatal("не добавлен по пересылке")
	}
}

func TestDelUser(t *testing.T) {
	b, api, _ := newTestBot(0)

	b.Handle(from(adminID, "/deluser"))
	kb := api.lastTo(t, adminID).ReplyMarkup.(tgbotapi.InlineKeyboardMarkup)
	if len(kb.InlineKeyboard) != 1 || *kb.InlineKeyboard[0][0].CallbackData != cbDelUser+"100" {
		t.Fatalf("кнопки: %+v", kb.InlineKeyboard)
	}
	b.Handle(adminCallback(cbDelUser + "100"))
	if b.cfg.Access.Allowed(userID) {
		t.Fatal("не удалён")
	}
	// userID в тестах задан как ALLOWED_USER_IDS — предупреждаем про .env.
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "уберите его из .env") {
		t.Errorf("удаление пользователя из env: %q", got)
	}
	b.Handle(text("/start", 0))
	if got := api.lastTo(t, userID).Text; !strings.Contains(got, "личный бот") {
		t.Errorf("удалённому: %q", got)
	}
	b.Handle(from(adminID, "/deluser 100"))
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "нет в списке") {
		t.Errorf("повторное удаление: %q", got)
	}
	b.Handle(from(adminID, "/users"))
	if got := api.lastTo(t, adminID).Text; !strings.Contains(got, "ни у кого нет") {
		t.Errorf("пустой список: %q", got)
	}
}

func TestAdminCommandsOnlyForAdmin(t *testing.T) {
	b, api, _ := newTestBot(0)

	for _, cmd := range []string{"/newuser 777", "/users", "/deluser 100", btnNewUser} {
		b.Handle(text(cmd, 0))
		if got := api.lastTo(t, userID).Text; !strings.HasPrefix(got, "Не понял") {
			t.Errorf("%s у обычного пользователя: %q", cmd, got)
		}
	}
	if b.cfg.Access.Allowed(777) || !b.cfg.Access.Allowed(userID) {
		t.Fatal("обычный пользователь изменил список")
	}

	// Кнопка «Новый пользователь» — только на клавиатуре админа.
	b.Handle(text("/start", 0))
	b.Handle(from(adminID, "/start"))
	has := func(chat int64) bool {
		kb := api.lastTo(t, chat).ReplyMarkup.(tgbotapi.ReplyKeyboardMarkup)
		for _, row := range kb.Keyboard {
			for _, btn := range row {
				if btn.Text == btnNewUser {
					return true
				}
			}
		}
		return false
	}
	if has(userID) || !has(adminID) {
		t.Errorf("кнопка: у пользователя %v, у админа %v", has(userID), has(adminID))
	}
}
