// Package storage хранит итоговые измерения давления в MongoDB.
package storage

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/zavgorodniyvv/presureBot/internal/pressure"
)

// Storage — то, что нужно боту от хранилища.
type Storage interface {
	Save(ctx context.Context, m pressure.Measurement) (string, error)
	Delete(ctx context.Context, userID int64, id string) (bool, error)
	List(ctx context.Context, userID int64, from, to time.Time) ([]pressure.Measurement, error)
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
	client *mongo.Client
	coll   *mongo.Collection
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

	coll := client.Database(database).Collection("measurements")
	_, err = coll.Indexes().CreateOne(pingCtx, mongo.IndexModel{
		Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "measured_at", Value: 1}},
	})
	if err != nil {
		client.Disconnect(context.Background())
		return nil, fmt.Errorf("создание индекса: %w", err)
	}
	return &Mongo{client: client, coll: coll}, nil
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
