// Package storage хранит итоговые измерения давления в MongoDB.
package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zavgorodniyvv/presureBot/internal/pills"
	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

// Storage — то, что нужно боту от хранилища.
type Storage interface {
	Save(ctx context.Context, m pressure.Measurement) (string, error)
	Delete(ctx context.Context, userID int64, id string) (bool, error)
	List(ctx context.Context, userID int64, from, to time.Time) ([]pressure.Measurement, error)

	// Таблетки: состояние дня (date — pills.DateLayout по местному времени).
	PillDay(ctx context.Context, userID int64, date string) (pills.Day, error)
	// MarkPillTaken отмечает приём; повторная отметка ничего не меняет (first=false).
	MarkPillTaken(ctx context.Context, userID int64, date string, at time.Time) (day pills.Day, first bool, err error)
	SaveReminder(ctx context.Context, userID int64, date string, at time.Time, msgID int) error
	SetFinalSent(ctx context.Context, userID int64, date string) error
	// PillDays — дни в диапазоне дат [from, to] включительно.
	PillDays(ctx context.Context, userID int64, from, to string) ([]pills.Day, error)
	// PillSchedule — расписание пользователя; ok=false, если он его не задавал.
	PillSchedule(ctx context.Context, userID int64) (schedule string, ok bool, err error)
	SetPillSchedule(ctx context.Context, userID int64, schedule string) error
}

// doc — документ в коллекции measurements.
type doc struct {
	ID            bson.ObjectID `bson:"_id,omitempty"`
	UserID        int64         `bson:"user_id"`
	MeasuredAt    time.Time     `bson:"measured_at"`
	Systolic      int           `bson:"systolic"`
	Diastolic     int           `bson:"diastolic"`
	Pulse         int           `bson:"pulse,omitempty"` // нет поля — пульс не распознан
	ReadingsCount int           `bson:"readings_count"`
	CreatedAt     time.Time     `bson:"created_at"`
}

type Mongo struct {
	client   *mongo.Client
	coll     *mongo.Collection
	pillDays *mongo.Collection
	settings *mongo.Collection
}

// NewMongo подключается к базe и создаёт индекс, если его ещё нет.
func NewMongo(ctx context.Context, uri, database string) (*Mongo, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("подключение к MongoDB: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		client.Disconnect(context.Background())
		return nil, fmt.Errorf("подключение к MongoDB: %w", err)
	}

	db := client.Database(database)
	s := &Mongo{
		client:   client,
		coll:     db.Collection("measurements"),
		pillDays: db.Collection("pill_days"),
		settings: db.Collection("settings"),
	}
	_, err = s.coll.Indexes().CreateOne(pingCtx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "measured_at", Value: 1}},
	})
	if err == nil {
		_, err = s.pillDays.Indexes().CreateOne(pingCtx, mongo.IndexModel{
			Keys:    bson.D{{Key: "user_id", Value: 1}, {Key: "date", Value: 1}},
			Options: options.Index().SetUnique(true),
		})
	}
	if err != nil {
		client.Disconnect(context.Background())
		return nil, fmt.Errorf("создание индекса: %w", err)
	}
	return s, nil
}

func (s *Mongo) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.client.Disconnect(ctx)
}

func (s *Mongo) Save(ctx context.Context, m pressure.Measurement) (string, error) {
	res, err := s.coll.InsertOne(ctx, doc{
		UserID:        m.UserID,
		MeasuredAt:    m.MeasuredAt,
		Systolic:      m.Systolic,
		Diastolic:     m.Diastolic,
		Pulse:         m.Pulse,
		ReadingsCount: m.ReadingsCount,
		CreatedAt:     time.Now(),
	})
	if err != nil {
		return "", err
	}
	return res.InsertedID.(bson.ObjectID).Hex(), nil
}

func (s *Mongo) Delete(ctx context.Context, userID int64, id string) (bool, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return false, nil // такой записи точно нет
	}
	res, err := s.coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: oid}, {Key: "user_id", Value: userID}})
	if err != nil {
		return false, err
	}
	return res.DeletedCount > 0, nil
}

// List возвращает измерения пользователя в полуинтервале [from, to), по времени.
func (s *Mongo) List(ctx context.Context, userID int64, from, to time.Time) ([]pressure.Measurement, error) {
	filter := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "measured_at", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lt", Value: to}}},
	}
	cur, err := s.coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "measured_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []doc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}

	out := make([]pressure.Measurement, len(docs))
	for i, d := range docs {
		out[i] = pressure.Measurement{
			ID:            d.ID.Hex(),
			UserID:        d.UserID,
			MeasuredAt:    d.MeasuredAt,
			Systolic:      d.Systolic,
			Diastolic:     d.Diastolic,
			Pulse:         d.Pulse,
			ReadingsCount: d.ReadingsCount,
		}
	}
	return out, nil
}

// pillDoc — документ в коллекции pill_days, один на пользователя и день.
type pillDoc struct {
	UserID            int64     `bson:"user_id"`
	Date              string    `bson:"date"`
	TakenAt           time.Time `bson:"taken_at,omitempty"`
	LastReminderAt    time.Time `bson:"last_reminder_at,omitempty"`
	LastReminderMsgID int       `bson:"last_reminder_msg_id,omitempty"`
	FinalSent         bool      `bson:"final_sent,omitempty"`
}

func (d pillDoc) day() pills.Day {
	return pills.Day{
		Date:              d.Date,
		TakenAt:           d.TakenAt,
		LastReminderAt:    d.LastReminderAt,
		LastReminderMsgID: d.LastReminderMsgID,
		FinalSent:         d.FinalSent,
	}
}

func dayKey(userID int64, date string) bson.D {
	return bson.D{{Key: "user_id", Value: userID}, {Key: "date", Value: date}}
}

func (s *Mongo) PillDay(ctx context.Context, userID int64, date string) (pills.Day, error) {
	var d pillDoc
	err := s.pillDays.FindOne(ctx, dayKey(userID, date)).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return pills.Day{Date: date}, nil
	}
	if err != nil {
		return pills.Day{}, err
	}
	return d.day(), nil
}

func (s *Mongo) MarkPillTaken(ctx context.Context, userID int64, date string, at time.Time) (pills.Day, bool, error) {
	// Ставим taken_at, только если его ещё нет. Если документ уже отмечен,
	// фильтр его не найдёт и upsert попытается вставить дубль — уникальный
	// индекс вернёт ошибку, это и значит «уже отмечено».
	filter := append(dayKey(userID, date), bson.E{Key: "taken_at", Value: bson.D{{Key: "$exists", Value: false}}})
	_, err := s.pillDays.UpdateOne(ctx, filter,
		bson.D{{Key: "$set", Value: bson.D{{Key: "taken_at", Value: at}}}},
		options.UpdateOne().SetUpsert(true))
	first := err == nil
	if err != nil && !mongo.IsDuplicateKeyError(err) {
		return pills.Day{}, false, err
	}
	day, err := s.PillDay(ctx, userID, date)
	return day, first, err
}

func (s *Mongo) SaveReminder(ctx context.Context, userID int64, date string, at time.Time, msgID int) error {
	_, err := s.pillDays.UpdateOne(ctx, dayKey(userID, date),
		bson.D{{Key: "$set", Value: bson.D{{Key: "last_reminder_at", Value: at}, {Key: "last_reminder_msg_id", Value: msgID}}}},
		options.UpdateOne().SetUpsert(true))
	return err
}

func (s *Mongo) SetFinalSent(ctx context.Context, userID int64, date string) error {
	_, err := s.pillDays.UpdateOne(ctx, dayKey(userID, date),
		bson.D{{Key: "$set", Value: bson.D{{Key: "final_sent", Value: true}}}},
		options.UpdateOne().SetUpsert(true))
	return err
}

func (s *Mongo) PillDays(ctx context.Context, userID int64, from, to string) ([]pills.Day, error) {
	filter := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "date", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lte", Value: to}}},
	}
	cur, err := s.pillDays.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "date", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []pillDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]pills.Day, len(docs))
	for i, d := range docs {
		out[i] = d.day()
	}
	return out, nil
}

func (s *Mongo) PillSchedule(ctx context.Context, userID int64) (string, bool, error) {
	var d struct {
		PillSchedule *string `bson:"pill_schedule"`
	}
	err := s.settings.FindOne(ctx, bson.D{{Key: "_id", Value: userID}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) || (err == nil && d.PillSchedule == nil) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return *d.PillSchedule, true, nil
}

func (s *Mongo) SetPillSchedule(ctx context.Context, userID int64, schedule string) error {
	_, err := s.settings.UpdateOne(ctx, bson.D{{Key: "_id", Value: userID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "pill_schedule", Value: schedule}}}},
		options.UpdateOne().SetUpsert(true))
	return err
}
