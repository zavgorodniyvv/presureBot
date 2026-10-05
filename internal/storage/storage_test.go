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
