// Package bot — обработчики Telegram: серии замеров, распознавание фото, график.
package bot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/chart"
	"github.com/zavgorodniyvv/presureBot/internal/pressure"
	"github.com/zavgorodniyvv/presureBot/internal/recognize"
	"github.com/zavgorodniyvv/presureBot/internal/storage"
)

const (
	btnStart  = "▶️ Начать измерение"
	btnFinish = "⏹ Закончить измерения"
	btnChart  = "📈 График"

	cbDropReading = "drop:"  // удалить замер из текущей серии
	cbDeleteSaved = "del:"   // удалить сохранённое измерение из базы
	cbChart       = "chart:" // показать график за N дней
)

var chartPeriods = []int{7, 30, 90, 365}

// manualRe — ручной ввод: «132/84», «132 84 71», «132/84/71».
var manualRe = regexp.MustCompile(`^\s*(\d{2,3})\s*[/\\ ]\s*(\d{2,3})(?:\s*[/\\ ]\s*(\d{2,3}))?\s*$`)

// API — часть tgbotapi.BotAPI, которой пользуется бот (подменяется в тестах).
type API interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
	GetFileDirectURL(fileID string) (string, error)
}

// Config — настройки поведения бота.
type Config struct {
	AllowedUsers   map[int64]bool
	Location       *time.Location
	TrendHalfLife  time.Duration // период полураспада веса в линии тренда
	SessionTimeout time.Duration // через сколько бездействия серия сохраняется сама
}

type entry struct {
	id      int64
	reading pressure.Reading
}

// session — незавершённая серия замеров в одном чате. Хранится в памяти:
// при перезапуске бота незавершённая серия теряется.
type session struct {
	userID  int64
	entries []entry
	timer   *time.Timer
}

type Bot struct {
	api        API
	recognizer recognize.Recognizer
	store      storage.Storage
	cfg        Config
	download   func(ctx context.Context, url string) ([]byte, error)
	now        func() time.Time

	mu       sync.Mutex
	sessions map[int64]*session // ключ — chat ID
	nextID   int64
}

func New(api API, rec recognize.Recognizer, st storage.Storage, cfg Config) *Bot {
	return &Bot{
		api:        api,
		recognizer: rec,
		store:      st,
		cfg:        cfg,
		download:   httpDownload,
		now:        time.Now,
		sessions:   make(map[int64]*session),
	}
}

// Handle обрабатывает одно обновление. Можно вызывать конкурентно.
func (b *Bot) Handle(u tgbotapi.Update) {
	switch {
	case u.CallbackQuery != nil:
		if b.allowed(u.CallbackQuery.From) {
			b.handleCallback(u.CallbackQuery)
		}
	case u.Message != nil:
		if b.allowed(u.Message.From) {
			b.handleMessage(u.Message)
		} else if u.Message.From != nil {
			log.Printf("доступ запрещён: user %d (@%s)", u.Message.From.ID, u.Message.From.UserName)
			b.reply(u.Message.Chat.ID, fmt.Sprintf("Это личный бот. Ваш Telegram ID: %d", u.Message.From.ID))
		}
	}
}

func (b *Bot) allowed(u *tgbotapi.User) bool {
	return u != nil && b.cfg.AllowedUsers[u.ID]
}

func (b *Bot) handleMessage(m *tgbotapi.Message) {
	chatID := m.Chat.ID

	if img, mime := imageOf(m); img != "" {
		b.handlePhoto(m, img, mime)
		return
	}

	text := strings.TrimSpace(m.Text)
	switch {
	case text == "/start" || text == "/help":
		b.send(withKeyboard(tgbotapi.NewMessage(chatID, helpText)))
	case text == btnStart || text == "/begin":
		b.startSession(chatID, m.From.ID)
	case text == btnFinish || text == "/end":
		b.finishSession(chatID, false)
	case text == btnChart || text == "/chart":
		b.askChartPeriod(chatID)
	case manualRe.MatchString(text):
		b.handleManual(m, text)
	default:
		b.send(withKeyboard(tgbotapi.NewMessage(chatID,
			"Не понял. Пришлите фото экрана тонометра, введите давление вручную (например 132/84 71) или воспользуйтесь кнопками.")))
	}
}

const helpText = `Бот записывает давление по фото экрана тонометра.

1. Нажмите «Начать измерение».
2. Присылайте фото экрана: одно или несколько замеров подряд. Если фото не распознаётся, введите значения текстом: 132/84 71 (верхнее/нижнее пульс).
3. Нажмите «Закончить измерения» — в базу запишется одно значение.

Как считается итог серии: при 1–2 замерах — среднее; при 3 и больше первый замер отбрасывается (он обычно завышен), остальные усредняются.

«График» — давление за период: красное — верхнее, синее — нижнее, пунктир — тренд (взвешенное среднее).`

// imageOf возвращает file ID картинки: сжатого фото или изображения, отправленного файлом.
func imageOf(m *tgbotapi.Message) (fileID, mime string) {
	if len(m.Photo) > 0 {
		return m.Photo[len(m.Photo)-1].FileID, "image/jpeg" // последний размер — самый крупный
	}
	if d := m.Document; d != nil && strings.HasPrefix(d.MimeType, "image/") {
		return d.FileID, d.MimeType
	}
	return "", ""
}

// startSession начинает серию; если серия уже идёт, только напоминает о ней.
func (b *Bot) startSession(chatID, userID int64) {
	b.mu.Lock()
	s, exists := b.sessions[chatID]
	n := 0
	if exists {
		n = len(s.entries)
	} else {
		b.sessions[chatID] = &session{userID: userID}
	}
	b.mu.Unlock()

	if exists {
		b.reply(chatID, fmt.Sprintf("Измерение уже идёт, замеров: %d. Присылайте фото или нажмите «Закончить измерения».", n))
		return
	}
	b.reply(chatID, "Присылайте фото экрана тонометра — можно несколько замеров подряд. Когда закончите, нажмите «Закончить измерения».")
}

func (b *Bot) handlePhoto(m *tgbotapi.Message, fileID, mime string) {
	chatID := m.Chat.ID
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	url, err := b.api.GetFileDirectURL(fileID)
	if err != nil {
		log.Printf("получение ссылки на файл: %v", err)
		b.reply(chatID, "Не удалось получить фото из Telegram, попробуйте ещё раз.")
		return
	}
	img, err := b.download(ctx, url)
	if err != nil {
		log.Printf("скачивание фото: %v", err)
		b.reply(chatID, "Не удалось скачать фото, попробуйте ещё раз.")
		return
	}

	res, err := b.recognizer.Recognize(ctx, img, mime)
	if err != nil {
		if !errors.Is(err, recognize.ErrNotRecognized) {
			log.Printf("распознавание: %v", err)
		}
		b.replyTo(chatID, m.MessageID, "Не получилось распознать показания. Переснимите экран крупнее и без бликов или введите вручную: 132/84 71")
		return
	}

	b.addReading(m, pressure.Reading{Systolic: res.Systolic, Diastolic: res.Diastolic, Pulse: res.Pulse, At: m.Time(), Seq: m.MessageID})
}

func (b *Bot) handleManual(m *tgbotapi.Message, text string) {
	g := manualRe.FindStringSubmatch(text)
	sys, _ := strconv.Atoi(g[1])
	dia, _ := strconv.Atoi(g[2])
	pulse, _ := strconv.Atoi(g[3]) // пустая группа → 0
	b.addReading(m, pressure.Reading{Systolic: sys, Diastolic: dia, Pulse: pulse, At: m.Time(), Seq: m.MessageID})
}

func (b *Bot) addReading(m *tgbotapi.Message, r pressure.Reading) {
	chatID := m.Chat.ID
	if err := r.Validate(); err != nil {
		b.replyTo(chatID, m.MessageID, fmt.Sprintf("Похоже на ошибку распознавания (%s): %s. Замер не записан — переснимите или введите вручную.", r, err))
		return
	}

	b.mu.Lock()
	s, ok := b.sessions[chatID]
	autoStarted := !ok
	if !ok {
		s = &session{userID: m.From.ID}
		b.sessions[chatID] = s
	}
	b.nextID++
	id := b.nextID
	s.entries = append(s.entries, entry{id: id, reading: r})
	n := len(s.entries)
	b.resetTimerLocked(chatID, s)
	b.mu.Unlock()

	text := fmt.Sprintf("Замер %d: %s", n, r)
	if autoStarted {
		text += "\n\nИзмерение начато автоматически. Пришлите ещё фото или нажмите «Закончить измерения»."
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = m.MessageID
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("❌ Убрать этот замер", cbDropReading+strconv.FormatInt(id, 10)),
	))
	b.send(msg)
}

// resetTimerLocked перезапускает таймер автосохранения серии. Вызывать под b.mu.
func (b *Bot) resetTimerLocked(chatID int64, s *session) {
	if b.cfg.SessionTimeout <= 0 {
		return
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(b.cfg.SessionTimeout, func() { b.finishSession(chatID, true) })
}

// finishSession сохраняет серию одним усреднённым измерением.
// auto — серия закрывается по таймеру бездействия.
func (b *Bot) finishSession(chatID int64, auto bool) {
	b.mu.Lock()
	s, ok := b.sessions[chatID]
	if !ok || len(s.entries) == 0 {
		delete(b.sessions, chatID)
		b.mu.Unlock()
		if !auto {
			b.send(withKeyboard(tgbotapi.NewMessage(chatID, "Нет замеров для сохранения. Нажмите «Начать измерение» и пришлите фото.")))
		}
		return
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	// Забираем серию из карты до записи в базу: фото, пришедшее во время
	// сохранения, начнёт новую серию, а не потеряется в старой.
	delete(b.sessions, chatID)
	b.mu.Unlock()

	readings := make([]pressure.Reading, len(s.entries))
	for i, e := range s.entries {
		readings[i] = e.reading
	}
	m, _ := pressure.Average(readings) // ошибка только для пустой серии
	m.UserID = s.userID

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	id, err := b.store.Save(ctx, m)
	if err != nil {
		log.Printf("сохранение измерения: %v", err)
		// Возвращаем серию, чтобы можно было повторить «Закончить».
		b.mu.Lock()
		if cur, ok := b.sessions[chatID]; ok {
			cur.entries = append(s.entries, cur.entries...)
		} else {
			s.timer = nil
			b.sessions[chatID] = s
		}
		b.mu.Unlock()
		b.reply(chatID, "Не удалось сохранить в базу. Замеры не потеряны — нажмите «Закончить измерения» ещё раз чуть позже.")
		return
	}

	var sb strings.Builder
	if auto {
		sb.WriteString("Серия закрыта автоматически после паузы.\n")
	}
	fmt.Fprintf(&sb, "Записано: %s\n", m)
	fmt.Fprintf(&sb, "Время: %s\n", m.MeasuredAt.In(b.cfg.Location).Format("02.01.2006 15:04"))
	switch n := len(readings); {
	case n >= 3:
		fmt.Fprintf(&sb, "Замеров: %d — первый отброшен, остальные усреднены.", n)
	case n == 2:
		sb.WriteString("Замеров: 2 — среднее.")
	default:
		sb.WriteString("Один замер.")
	}

	msg := tgbotapi.NewMessage(chatID, sb.String())
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("🗑 Удалить запись", cbDeleteSaved+id),
	))
	b.send(msg)
}

func (b *Bot) handleCallback(q *tgbotapi.CallbackQuery) {
	if q.Message == nil {
		return
	}
	chatID := q.Message.Chat.ID
	answer := ""

	switch {
	case strings.HasPrefix(q.Data, cbDropReading):
		id, _ := strconv.ParseInt(strings.TrimPrefix(q.Data, cbDropReading), 10, 64)
		if b.dropReading(chatID, id) {
			answer = "Замер убран"
			b.editText(q.Message, "<s>"+escape(q.Message.Text)+"</s>\nЗамер убран из серии.")
		} else {
			answer = "Этой серии уже нет"
		}

	case strings.HasPrefix(q.Data, cbDeleteSaved):
		id := strings.TrimPrefix(q.Data, cbDeleteSaved)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		ok, err := b.store.Delete(ctx, q.From.ID, id)
		cancel()
		switch {
		case err != nil:
			log.Printf("удаление измерения %s: %v", id, err)
			answer = "Ошибка базы, попробуйте позже"
		case ok:
			answer = "Запись удалена"
			b.editText(q.Message, "<s>"+escape(q.Message.Text)+"</s>\nЗапись удалена.")
		default:
			answer = "Запись уже удалена"
		}

	case strings.HasPrefix(q.Data, cbChart):
		days, _ := strconv.Atoi(strings.TrimPrefix(q.Data, cbChart))
		b.request(tgbotapi.NewCallback(q.ID, "Рисую…"))
		b.sendChart(chatID, q.From.ID, days)
		return
	}

	b.request(tgbotapi.NewCallback(q.ID, answer))
}

func (b *Bot) dropReading(chatID, id int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[chatID]
	if !ok {
		return false
	}
	for i, e := range s.entries {
		if e.id == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

func (b *Bot) askChartPeriod(chatID int64) {
	row := make([]tgbotapi.InlineKeyboardButton, 0, len(chartPeriods))
	for _, d := range chartPeriods {
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(periodName(d), cbChart+strconv.Itoa(d)))
	}
	msg := tgbotapi.NewMessage(chatID, "За какой период?")
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(row)
	b.send(msg)
}

func periodName(days int) string {
	switch days {
	case 7:
		return "Неделя"
	case 30:
		return "Месяц"
	case 90:
		return "3 месяца"
	case 365:
		return "Год"
	}
	return fmt.Sprintf("%d дн.", days)
}

func (b *Bot) sendChart(chatID, userID int64, days int) {
	if days <= 0 || days > 3660 {
		days = 30
	}
	loc := b.cfg.Location
	now := b.now().In(loc)
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	from := to.AddDate(0, 0, -days)

	// Тренд считаем по истории с запасом, чтобы в начале периода он не начинался с нуля.
	history := from.Add(-4 * b.cfg.TrendHalfLife)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	all, err := b.store.List(ctx, userID, history, to)
	if err != nil {
		log.Printf("чтение измерений: %v", err)
		b.reply(chatID, "Не удалось прочитать данные из базы, попробуйте позже.")
		return
	}

	trendAll := pressure.Trend(all, b.cfg.TrendHalfLife)
	var ms []pressure.Measurement
	var trend []pressure.TrendPoint
	for i, m := range all {
		if !m.MeasuredAt.Before(from) {
			ms = append(ms, m)
			trend = append(trend, trendAll[i])
		}
	}

	title := fmt.Sprintf("Давление: %s — %s", from.Format("02.01.2006"), to.AddDate(0, 0, -1).Format("02.01.2006"))
	img, err := chart.Render(ms, trend, chart.Options{From: from, To: to, Location: loc, Title: title})
	if errors.Is(err, chart.ErrNoData) {
		b.reply(chatID, fmt.Sprintf("За период «%s» измерений нет.", strings.ToLower(periodName(days))))
		return
	}
	if err != nil {
		log.Printf("рендер графика: %v", err)
		b.reply(chatID, "Не удалось нарисовать график.")
		return
	}

	photo := tgbotapi.NewPhoto(chatID, tgbotapi.FileBytes{Name: "pressure.png", Bytes: img})
	photo.Caption = summary(ms)
	b.send(photo)
}

// summary — подпись к графику: среднее и разброс за период.
func summary(ms []pressure.Measurement) string {
	var sys, dia, pulse, pulseN int
	minS, maxS := ms[0].Systolic, ms[0].Systolic
	for _, m := range ms {
		sys += m.Systolic
		dia += m.Diastolic
		if m.Pulse > 0 {
			pulse += m.Pulse
			pulseN++
		}
		minS = min(minS, m.Systolic)
		maxS = max(maxS, m.Systolic)
	}
	n := len(ms)
	s := fmt.Sprintf("Измерений: %d\nСреднее: %d/%d", n, (sys+n/2)/n, (dia+n/2)/n)
	if pulseN > 0 {
		s += fmt.Sprintf(", пульс %d", (pulse+pulseN/2)/pulseN)
	}
	s += fmt.Sprintf("\nВерхнее: от %d до %d", minS, maxS)
	return s
}

func withKeyboard(msg tgbotapi.MessageConfig) tgbotapi.MessageConfig {
	kb := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(btnStart), tgbotapi.NewKeyboardButton(btnFinish)),
		tgbotapi.NewKeyboardButtonRow(tgbotapi.NewKeyboardButton(btnChart)),
	)
	kb.ResizeKeyboard = true
	msg.ReplyMarkup = kb
	return msg
}

func (b *Bot) reply(chatID int64, text string) {
	b.send(tgbotapi.NewMessage(chatID, text))
}

func (b *Bot) replyTo(chatID int64, msgID int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = msgID
	b.send(msg)
}

func (b *Bot) editText(m *tgbotapi.Message, html string) {
	edit := tgbotapi.NewEditMessageText(m.Chat.ID, m.MessageID, html)
	edit.ParseMode = tgbotapi.ModeHTML
	b.send(edit)
}

func (b *Bot) send(c tgbotapi.Chattable) {
	if _, err := b.api.Send(c); err != nil {
		log.Printf("отправка сообщения: %v", err)
	}
}

func (b *Bot) request(c tgbotapi.Chattable) {
	if _, err := b.api.Request(c); err != nil {
		log.Printf("запрос к Telegram: %v", err)
	}
}

func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

func httpDownload(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("статус %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
}
