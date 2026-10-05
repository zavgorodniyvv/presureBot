package storage

import (
	"context"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

// Интеграционный тест: запускается, только если задан TEST_MONGODB_URI,
// например mongodb://localhost:57017. Работает в базе pressurebot_test.
func TestMongo(t *testing.T) {
	uri := os.Getenv("TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("TEST_MONGODB_URI не задан")
	}
	ctx := context.Background()

	st, err := NewMongo(ctx, uri, "pressurebot_test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Индекс создаётся идемпотентно — повторное подключение не должно падать.
	st2, err := NewMongo(ctx, uri, "pressurebot_test")
	if err != nil {
		t.Fatal(err)
	}
	st2.Close()

	const user = int64(-42) // отрицательный id, чтобы не пересечься с реальными
	if _, err := st.coll.DeleteMany(ctx, bson.D{{Key: "user_id", Value: user}}); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	id1, err := st.Save(ctx, pressure.Measurement{UserID: user, MeasuredAt: t0, Systolic: 130, Diastolic: 85, Pulse: 70, ReadingsCount: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Save(ctx, pressure.Measurement{UserID: user, MeasuredAt: t0.Add(-time.Hour), Systolic: 120, Diastolic: 80, ReadingsCount: 1}); err != nil {
		t.Fatal(err)
	}

	got, err := st.List(ctx, user, t0.Add(-24*time.Hour), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Systolic != 120 || got[0].Pulse != 0 || got[1].Pulse != 70 || got[1].ReadingsCount != 3 || got[1].ID != id1 {
		t.Fatalf("неожиданный результат: %+v", got)
	}
	if !got[1].MeasuredAt.Equal(t0) {
		t.Errorf("время: %v", got[1].MeasuredAt)
	}

	// Граница to не включается.
	got, _ = st.List(ctx, user, t0.Add(-24*time.Hour), t0)
	if len(got) != 1 {
		t.Errorf("полуинтервал: %d записей", len(got))
	}

	if ok, err := st.Delete(ctx, user+1, id1); err != nil || ok {
		t.Errorf("чужую запись удалять нельзя: ok=%v err=%v", ok, err)
	}
	if ok, err := st.Delete(ctx, user, "не-objectid"); err != nil || ok {
		t.Errorf("кривой id: ok=%v err=%v", ok, err)
	}
	if ok, err := st.Delete(ctx, user, id1); err != nil || !ok {
		t.Errorf("удаление: ok=%v err=%v", ok, err)
	}
	got, _ = st.List(ctx, user, t0.Add(-24*time.Hour), t0.Add(time.Hour))
	if len(got) != 1 {
		t.Errorf("после удаления осталось %d", len(got))
	}
}

func TestMongoPills(t *testing.T) {
	uri := os.Getenv("TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("TEST_MONGODB_URI не задан")
	}
	ctx := context.Background()
	st, err := NewMongo(ctx, uri, "pressurebot_test")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	const user = int64(-43)
	st.pillDays.DeleteMany(ctx, bson.D{{Key: "user_id", Value: user}})
	st.settings.DeleteMany(ctx, bson.D{{Key: "_id", Value: user}})

	d, err := st.PillDay(ctx, user, "2026-10-06")
	if err != nil || d.Taken() || d.Date != "2026-10-06" {
		t.Fatalf("пустой день: %+v %v", d, err)
	}

	t0 := time.Date(2026, 10, 6, 1, 30, 0, 0, time.UTC)
	if err := st.SaveReminder(ctx, user, "2026-10-06", t0, 555); err != nil {
		t.Fatal(err)
	}
	d, first, err := st.MarkPillTaken(ctx, user, "2026-10-06", t0.Add(5*time.Minute))
	if err != nil || !first || !d.TakenAt.Equal(t0.Add(5*time.Minute)) || d.LastReminderMsgID != 555 {
		t.Fatalf("первая отметка: %+v first=%v err=%v", d, first, err)
	}
	// Повторная отметка не меняет время.
	d, first, err = st.MarkPillTaken(ctx, user, "2026-10-06", t0.Add(time.Hour))
	if err != nil || first || !d.TakenAt.Equal(t0.Add(5*time.Minute)) {
		t.Fatalf("повторная отметка: %+v first=%v err=%v", d, first, err)
	}
	// Отметка без предшествующего напоминания (upsert).
	if _, first, err := st.MarkPillTaken(ctx, user, "2026-10-07", t0); err != nil || !first {
		t.Fatalf("отметка нового дня: first=%v err=%v", first, err)
	}
	if err := st.SetFinalSent(ctx, user, "2026-10-08"); err != nil {
		t.Fatal(err)
	}

	days, err := st.PillDays(ctx, user, "2026-10-06", "2026-10-08")
	if err != nil || len(days) != 3 || !days[0].Taken() || days[2].Taken() || !days[2].FinalSent {
		t.Fatalf("PillDays: %+v %v", days, err)
	}

	if _, ok, err := st.PillSchedule(ctx, user); err != nil || ok {
		t.Fatalf("расписание до записи: ok=%v err=%v", ok, err)
	}
	if err := st.SetPillSchedule(ctx, user, "пн 07:00"); err != nil {
		t.Fatal(err)
	}
	if s, ok, err := st.PillSchedule(ctx, user); err != nil || !ok || s != "пн 07:00" {
		t.Fatalf("расписание: %q ok=%v err=%v", s, ok, err)
	}
}
