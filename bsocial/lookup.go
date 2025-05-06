package bsocial

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/b-open-io/overlay/publish"
	"github.com/bitcoinschema/go-bmap"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/overlay/lookup"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type LookupService struct {
	db  *mongo.Database
	pub publish.Publisher
}

func NewLookupService(connString string, dbName string, pub publish.Publisher) (*LookupService, error) {
	clientOptions := options.Client().ApplyURI(connString).SetMaxPoolSize(100)
	if client, err := mongo.Connect(context.Background(), clientOptions); err != nil {
		return nil, err
	} else {
		db := client.Database(dbName)
		if _, err := db.Collection("post").Indexes().CreateMany(
			context.Background(),
			[]mongo.IndexModel{
				{Keys: bson.M{"timestamp": -1}},
				{Keys: bson.D{{Key: "AIP.address", Value: 1}, {Key: "timestamp", Value: -1}}, Options: options.Index().SetSparse(true)},
				{Keys: bson.D{{Key: "MAP.tx", Value: 1}}, Options: options.Index().SetSparse(true)},
			},
		); err != nil {
			return nil, err
		}
		if _, err := db.Collection("like").Indexes().CreateMany(
			context.Background(),
			[]mongo.IndexModel{
				{Keys: bson.M{"timestamp": -1}},
				{Keys: bson.D{{Key: "AIP.address", Value: 1}, {Key: "timestamp", Value: -1}}, Options: options.Index().SetSparse(true)},
				{Keys: bson.D{{Key: "MAP.tx", Value: 1}, {Key: "timestamp", Value: -1}}, Options: options.Index().SetSparse(true)},
			},
		); err != nil {
			return nil, err
		}
		return &LookupService{
			db:  db,
			pub: pub,
		}, nil
	}
}

func (l *LookupService) OutputAdded(ctx context.Context, outpoint *overlay.Outpoint, topic string, beef []byte) (err error) {
	_, tx, _, err := transaction.ParseBeef(beef)
	if err != nil {
		return err
	}

	bmapTx, err := bmap.NewFromTx(tx)
	if err != nil {
		return err
	}
	if tx.MerklePath != nil {
		bmapTx.Blk.I = tx.MerklePath.BlockHeight
	}
	bsonData, err := PrepareForIngestion(bmapTx)

	var collectionName string
	var ok bool
	if collectionName, ok = bsonData["collection"].(string); !ok {
		return
	}
	delete(bsonData, "collection")
	collection := l.db.Collection(collectionName)
	_, err = collection.UpdateOne(
		ctx,
		bson.M{"_id": bsonData["_id"]},
		bson.M{
			"$set": bsonData,
			"$setOnInsert": bson.M{
				"timestamp": time.Now().UnixMilli(),
			},
		},
		options.Update().SetUpsert(true),
	)
	return err

}
func (l *LookupService) OutputSpent(ctx context.Context, outpoint *overlay.Outpoint, topic string, beef []byte) error {
	// This function is intentionally left empty as the lookup service does not handle spent outputs.
	return nil
}
func (l *LookupService) OutputDeleted(ctx context.Context, outpoint *overlay.Outpoint, topic string) error {
	// This function is intentionally left empty as the lookup service does not handle deleted outputs.
	return nil
}
func (l *LookupService) OutputBlockHeightUpdated(ctx context.Context, outpoint *overlay.Outpoint, blockHeight uint32, blockIndex uint64) error {
	// This function is intentionally left empty as the lookup service does not handle block height updates.
	return nil
}
func (l *LookupService) Lookup(ctx context.Context, question *lookup.LookupQuestion) (*lookup.LookupAnswer, error) {
	// This function is intentionally left empty as the lookup service does not handle lookups.
	return nil, nil
}

func (l *LookupService) GetDocumentation() string {
	return "Events lookup"
}

func (l *LookupService) GetMetaData() *overlay.MetaData {
	return &overlay.MetaData{
		Name: "Events",
	}
}

func (l *LookupService) Search(ctx context.Context, query string, limit int, offset int) ([]map[string]any, error) {
	// Implementation for searching identities based on a query using Atlas Search $search aggregation
	pipeline := mongo.Pipeline{
		{ // First stage: $search
			bson.E{Key: "$search", Value: bson.D{
				{Key: "index", Value: "default"}, // Replace with your Atlas Search index name if different
				{Key: "wildcard", Value: bson.D{
					{Key: "query", Value: fmt.Sprintf("*%s*", query)},           // Use the input query, wrapped with wildcards
					{Key: "path", Value: bson.D{{Key: "wildcard", Value: "*"}}}, // Search across all fields
					{Key: "allowAnalyzedField", Value: true},                    // Optional: Can improve performance if fields are analyzed
				}},
			}},
		},
		{ // Second stage: $skip
			bson.E{Key: "$skip", Value: int64(offset)}, // Skip the specified number of documents
		},
		{ // Third stage: $limit
			bson.E{Key: "$limit", Value: int64(limit)}, // Limit the number of results
		},
		// Optional: Add other stages like $project if needed
	}

	cursor, err := l.db.Collection("post").Aggregate(ctx, pipeline)
	if err != nil {
		// Log the pipeline for debugging if needed
		// log.Printf("Search pipeline: %+v\n", pipeline)
		return nil, fmt.Errorf("failed to execute search aggregation: %w", err)
	}
	defer cursor.Close(ctx)

	var identities []map[string]any
	if err = cursor.All(ctx, &identities); err != nil {
		return nil, fmt.Errorf("failed to decode search results: %w", err)
	}

	return identities, nil
}

func PrepareForIngestion(bmapData *bmap.Tx) (bsonData bson.M, err error) {

	// delete input.Tape from the inputs and outputs
	for i := range bmapData.Tx.In {
		bmapData.Tx.In[i].Tape = nil
	}

	for i := range bmapData.Tx.Out {
		bmapData.Tx.Out[i].Tape = nil
	}

	bsonData = bson.M{
		"_id": bmapData.Tx.Tx.H,
		"tx":  bmapData.Tx.Tx,
		"blk": bmapData.Tx.Blk,
		"in":  bmapData.Tx.In,
		"out": bmapData.Tx.Out,
	}

	if len(bmapData.AIP) > 0 {
		// bsonData["AIP"] = bmapData.AIP
		aips := make([]bson.M, len(bmapData.AIP))
		for i, a := range bmapData.AIP {
			aips[i] = bson.M{
				"algorithm": a.Algorithm,
				"address":   a.AlgorithmSigningComponent,
				"indices":   a.Indices,
				"signature": a.Signature,
			}
		}
		bsonData["AIP"] = aips
	}

	if len(bmapData.Sigma) > 0 {
		bsonData["SIGMA"] = bmapData.Sigma
	}

	if bmapData.BAP != nil {
		bsonData["BAP"] = bmapData.BAP
	}

	if bmapData.Ord != nil {
		// remove the data
		for _, o := range bmapData.Ord {
			o.Data = []byte{}

			// take only the first 255 characters
			if len(o.ContentType) > 255 {
				o.ContentType = o.ContentType[:255]
			}
		}

		bsonData["Ord"] = bmapData.Ord
	}

	bs := []bson.M{}
	for _, b := range bmapData.B {
		item := bson.M{
			"content-type": b.MediaType,
			"encoding":     b.Encoding,
			"filename":     b.Filename,
		}

		if strings.HasPrefix(b.MediaType, "text") {
			if len(b.Data) > 256*1024 {
				item["content"] = string(b.Data[:256*1024])
			} else {
				item["content"] = string(b.Data)
			}
		}
		bs = append(bs, item)
	}
	bsonData["B"] = bs

	if bmapData.BOOST != nil {
		bsonData["BOOST"] = bmapData.BOOST
	}

	if bmapData.MAP == nil {
		log.Println("No MAP data.")
		return
	}

	bsonData["MAP"] = bmapData.MAP

	if collection, ok := bmapData.MAP[0]["type"].(string); ok {
		bsonData["collection"] = collection
	} else {
		// log.Println("Error: MAP 'type' key does not exist.")
		return
	}
	if _, ok := bmapData.MAP[0]["app"].(string); !ok {
		// log.Println("Error: MAP 'app' key does not exist.")
		return
	}

	for key, value := range bsonData {
		if str, ok := value.(string); ok {
			if !utf8.ValidString(str) {
				log.Printf("Invalid UTF-8 detected in key %s: %s", key, str)
				return
			}
		}
	}

	return
}
