package repo

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"gameserver/framework/storage/mongox"
	"gameserver/game/model"
)

// Collection names.
const (
	CollAccounts = "accounts"
	CollPlayers  = "players"
)

// MongoAccounts stores accounts in the "accounts" collection (_id = account name).
type MongoAccounts struct {
	coll *mongo.Collection
}

// NewMongoAccounts creates the repository. The _id index guarantees uniqueness.
func NewMongoAccounts(db *mongo.Database) *MongoAccounts {
	return &MongoAccounts{coll: db.Collection(CollAccounts)}
}

func (r *MongoAccounts) Get(ctx context.Context, name string) (*model.Account, error) {
	var acc model.Account
	err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: name}}).Decode(&acc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &acc, nil
}

func (r *MongoAccounts) Create(ctx context.Context, acc *model.Account) error {
	_, err := r.coll.InsertOne(ctx, acc)
	if mongox.IsDuplicateKey(err) {
		return ErrDuplicate
	}
	return err
}

// MongoPlayers stores players in the "players" collection (_id = uid).
type MongoPlayers struct {
	coll *mongo.Collection
}

// NewMongoPlayers creates the repository.
func NewMongoPlayers(db *mongo.Database) *MongoPlayers {
	return &MongoPlayers{coll: db.Collection(CollPlayers)}
}

func (r *MongoPlayers) Load(ctx context.Context, uid int64) (*model.Player, error) {
	var p model.Player
	err := r.coll.FindOne(ctx, bson.D{{Key: "_id", Value: uid}}).Decode(&p)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *MongoPlayers) Save(ctx context.Context, p *model.Player) error {
	_, err := r.coll.ReplaceOne(ctx, bson.D{{Key: "_id", Value: p.UID}}, p, options.Replace().SetUpsert(true))
	return err
}
