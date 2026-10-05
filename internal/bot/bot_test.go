package bot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/zavgorodniyvv/presureBot/internal/pills"
	"github.com/zavgorodniyvv/presureBot/internal/pressure"
	"github.com/zavgorodniyvv/presureBot/internal/recognize"
)

const (
	userID = int64(100)
	chatID = int64(100)
)

type fakeAPI struct {
	mu      sync.Mutex
	sent    []tgbotapi.Chattable
	deleted []int
	msgID   int
}

func (f *fakeAPI) Send(c tgbotapi.Chattable) (tgbotapi.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, c)
	f.msgID++
	return tgbotapi.Message{MessageID: 1000 + f.msgID}, nil
}

func (f *fakeAPI) Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := c.(tgbotapi.DeleteMessageConfig); ok {
		f.deleted = append(f.deleted, d.MessageID)
	}
	return &tgbotapi.APIResponse{Ok: true}, nil
}

func (f *fakeAPI) GetFileDirectURL(fileID string) (string, error) {
	return "https://example.invalid/" + fileID, nil
}

// lastText — текст последнего отправленного сообщения.
func (f *fakeAPI) lastText(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		t.Fatal("бот ничего не отправил")
	}
	switch m := f.sent[len(f.sent)-1].(type) {
	case tgbotapi.MessageConfig:
		return m.Text
	case tgbotapi.PhotoConfig:
		return m.Caption
	case tgbotapi.EditMessageTextConfig:
		return m.Text
	}
	return ""
}

// fakeRecognizer возвращает показания по file ID («132/84/71»), «bad» — ошибка.
type fakeRecognizer struct{}

func (fakeRecognizer) Recognize(_ context.Context, img []byte, _ string) (recognize.Result, error) {
	g := manualRe.FindStringSubmatch(strings.TrimPrefix(string(img), "https://example.invalid/"))
	if g == nil {
		return recognize.Result{}, recognize.ErrNotRecognized
	}
	var r recognize.Result
	r.Systolic, r.Diastolic, r.Pulse = atoi(g[1]), atoi(g[2]), atoi(g[3])
	return r, nil
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

type fakeStore struct {
	mu        sync.Mutex
	saved     []pressure.Measurement
	seq       int
	fail      bool
	pillDays  map[string]pills.Day // ключ — дата; тесты работают с одним пользователем
	schedules map[int64]string
}

func (s *fakeStore) PillDay(_ context.Context, _ int64, date string) (pills.Day, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.pillDays[date]
	if !ok {
		d.Date = date
	}
	return d, nil
}

func (s *fakeStore) update(date string, f func(*pills.Day)) {
	if s.pillDays == nil {
		s.pillDays = map[string]pills.Day{}
	}
	d := s.pillDays[date]
	d.Date = date
	f(&d)
	s.pillDays[date] = d
}

func (s *fakeStore) MarkPillTaken(_ context.Context, _ int64, date string, at time.Time) (pills.Day, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	first := !s.pillDays[date].Taken()
	if first {
		s.update(date, func(d *pills.Day) { d.TakenAt = at })
	}
	return s.pillDays[date], first, nil
}

func (s *fakeStore) SaveReminder(_ context.Context, _ int64, date string, at time.Time, msgID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.update(date, func(d *pills.Day) { d.LastReminderAt, d.LastReminderMsgID = at, msgID })
	return nil
}

func (s *fakeStore) SetFinalSent(_ context.Context, _ int64, date string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.update(date, func(d *pills.Day) { d.FinalSent = true })
	return nil
}

func (s *fakeStore) PillDays(_ context.Context, _ int64, from, to string) ([]pills.Day, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pills.Day
	for date, d := range s.pillDays {
		if date >= from && date <= to {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out, nil
}

func (s *fakeStore) PillSchedule(_ context.Context, uid int64) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.schedules[uid]
	return v, ok, nil
}

func (s *fakeStore) SetPillSchedule(_ context.Context, uid int64, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schedules == nil {
		s.schedules = map[int64]string{}
	}
	s.schedules[uid] = v
	return nil
}

func (s *fakeStore) Save(_ context.Context, m pressure.Measurement) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", errors.New("база недоступна")
	}
	s.seq++
	m.ID = fmt.Sprintf("id%d", s.seq)
	s.saved = append(s.saved, m)
	return m.ID, nil
}

func (s *fakeStore) Delete(_ context.Context, uid int64, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, m := range s.saved {
		if m.ID == id && m.UserID == uid {
			s.saved = append(s.saved[:i], s.saved[i+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) List(_ context.Context, uid int64, from, to time.Time) ([]pressure.Measurement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []pressure.Measurement
	for _, m := range s.saved {
		if m.UserID == uid && !m.MeasuredAt.Before(from) && m.MeasuredAt.Before(to) {
			out = append(out, m)
		}
	}
	return out, nil
}

var base = time.Date(2026, 10, 5, 7, 30, 0, 0, time.UTC)

func newTestBot(timeout time.Duration) (*Bot, *fakeAPI, *fakeStore) {
	api := &fakeAPI{}
	st := &fakeStore{}
	loc, _ := time.LoadLocation("Asia/Jerusalem")
	b := New(api, fakeRecognizer{}, st, Config{
		AllowedUsers:   map[int64]bool{userID: true},
		Location:       loc,
		TrendHalfLife:  3 * 24 * time.Hour,
		SessionTimeout: timeout,
		PillCutoff:     12 * time.Hour,
		PillRepeat:     10 * time.Minute,
	})
	// «Скачанная картинка» — это просто URL, по нему фейковый распознаватель отдаёт значения.
	b.download = func(_ context.Context, url string) ([]byte, error) { return []byte(url), nil }
	b.now = func() time.Time { return base.Add(time.Hour) }
	return b, api, st
}

var msgSeq int

func text(s string, minute int) tgbotapi.Update {
	msgSeq++
	return tgbotapi.Update{Message: &tgbotapi.Message{
		MessageID: msgSeq,
		From:      &tgbotapi.User{ID: userID},
		Chat:      &tgbotapi.Chat{ID: chatID},
		Date:      int(base.Add(time.Duration(minute) * time.Minute).Unix()),
		Text:      s,
	}}
}

func photo(fileID string, minute int) tgbotapi.Update {
	u := text("", minute)
	u.Message.Photo = []tgbotapi.PhotoSize{{FileID: "small"}, {FileID: fileID}}
	return u
}

func TestSessionAveragesThreeReadings(t *testing.T) {
	b, api, st := newTestBot(0)

	b.Handle(text(btnStart, 0))
	b.Handle(photo("150/95/80", 0))
	if got := api.lastText(t); !strings.Contains(got, "Замер 1: 150/95, пульс 80") {
		t.Errorf("ответ на фото: %q", got)
	}
	b.Handle(photo("130/84/70", 2))
	b.Handle(photo("126/80/68", 4))
	b.Handle(text(btnFinish, 5))

	if len(st.saved) != 1 {
		t.Fatalf("сохранено %d записей, ожидали 1", len(st.saved))
	}
	m := st.saved[0]
	if m.Systolic != 128 || m.Diastolic != 82 || m.Pulse != 69 || m.ReadingsCount != 3 || m.UserID != userID {
		t.Errorf("итог: %+v", m)
	}
	if !m.MeasuredAt.Equal(base) {
		t.Errorf("время = %v, ожидали время первого замера %v", m.MeasuredAt, base)
	}
	if got := api.lastText(t); !strings.Contains(got, "первый отброшен") || !strings.Contains(got, "10:30") {
		t.Errorf("итоговое сообщение: %q", got)
	}
}

func TestPhotoWithoutStartAutoStarts(t *testing.T) {
	b, api, st := newTestBot(0)

	b.Handle(photo("120/80/60", 0))
	if got := api.lastText(t); !strings.Contains(got, "начато автоматически") {
		t.Errorf("ответ: %q", got)
	}
	b.Handle(text(btnFinish, 1))
	if len(st.saved) != 1 || st.saved[0].Systolic != 120 {
		t.Fatalf("saved = %+v", st.saved)
	}
}

func TestManualInputAndUnrecognizedPhoto(t *testing.T) {
	b, api, st := newTestBot(0)

	b.Handle(text(btnStart, 0))
	b.Handle(photo("bad", 0))
	if got := api.lastText(t); !strings.Contains(got, "Не получилось распознать") {
		t.Errorf("ответ: %q", got)
	}
	b.Handle(text("131/85 70", 1))
	b.Handle(text(btnFinish, 2))
	if len(st.saved) != 1 || st.saved[0].Systolic != 131 || st.saved[0].Pulse != 70 {
		t.Fatalf("saved = %+v", st.saved)
	}
}

func TestInvalidReadingRejected(t *testing.T) {
	b, api, st := newTestBot(0)

	b.Handle(photo("80/120/60", 0)) // перепутаны верхнее и нижнее
	if got := api.lastText(t); !strings.Contains(got, "не записан") {
		t.Errorf("ответ: %q", got)
	}
	b.Handle(text(btnFinish, 1))
	if len(st.saved) != 0 {
		t.Fatalf("сохранили неверный замер: %+v", st.saved)
	}
}

func TestDropReading(t *testing.T) {
	b, _, st := newTestBot(0)

	b.Handle(photo("180/110/90", 0)) // ошибочный замер — уберём его
	b.Handle(photo("124/80/66", 1))
	b.Handle(tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID:      "q",
		From:    &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{MessageID: 1, Chat: &tgbotapi.Chat{ID: chatID}, Text: "Замер 1"},
		Data:    cbDropReading + "1",
	}})
	b.Handle(text(btnFinish, 2))

	if len(st.saved) != 1 || st.saved[0].Systolic != 124 || st.saved[0].ReadingsCount != 1 {
		t.Fatalf("saved = %+v", st.saved)
	}
}

func TestSaveFailureKeepsSession(t *testing.T) {
	b, api, st := newTestBot(0)

	b.Handle(photo("120/80/60", 0))
	st.fail = true
	b.Handle(text(btnFinish, 1))
	if got := api.lastText(t); !strings.Contains(got, "не потеряны") {
		t.Errorf("ответ: %q", got)
	}
	st.fail = false
	b.Handle(text(btnFinish, 2))
	if len(st.saved) != 1 {
		t.Fatalf("после повтора сохранено %d", len(st.saved))
	}
}

func TestSessionTimeoutSavesAutomatically(t *testing.T) {
	b, _, st := newTestBot(50 * time.Millisecond)

	b.Handle(photo("120/80/60", 0))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		n := len(st.saved)
		st.mu.Unlock()
		if n == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("серия не сохранилась по таймеру")
}

func TestForeignUserIgnored(t *testing.T) {
	b, api, st := newTestBot(0)

	u := photo("120/80/60", 0)
	u.Message.From.ID = 999
	b.Handle(u)
	if len(st.saved) != 0 {
		t.Fatal("чужой пользователь смог записать")
	}
	if got := api.lastText(t); !strings.Contains(got, "личный бот") {
		t.Errorf("ответ: %q", got)
	}
}

func TestChart(t *testing.T) {
	b, api, _ := newTestBot(0)

	b.Handle(text("/chart", 0))
	b.Handle(tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID: "q", From: &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: chatID}}, Data: cbChart + "30",
	}})
	if got := api.lastText(t); !strings.Contains(got, "измерений нет") {
		t.Errorf("пустой график: %q", got)
	}

	b.Handle(photo("130/85/70", -2*24*60))
	b.Handle(text(btnFinish, -2*24*60+1))
	b.Handle(photo("120/80/60", 0))
	b.Handle(text(btnFinish, 1))
	b.Handle(tgbotapi.Update{CallbackQuery: &tgbotapi.CallbackQuery{
		ID: "q", From: &tgbotapi.User{ID: userID},
		Message: &tgbotapi.Message{Chat: &tgbotapi.Chat{ID: chatID}}, Data: cbChart + "30",
	}})
	api.mu.Lock()
	last := api.sent[len(api.sent)-1]
	api.mu.Unlock()
	p, ok := last.(tgbotapi.PhotoConfig)
	if !ok {
		t.Fatalf("ожидали фото графика, получили %T", last)
	}
	if !strings.Contains(p.Caption, "Измерений: 2") || !strings.Contains(p.Caption, "Среднее: 125/83") {
		t.Errorf("подпись: %q", p.Caption)
	}
}
