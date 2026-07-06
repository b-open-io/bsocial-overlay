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
	"github.com/bsv-blockchain/go-overlay-services/pkg/core/engine"
	"github.com/bsv-blockchain/go-sdk/chainhash"
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
		if _, err := db.Collection("unlike").Indexes().CreateMany(
			context.Background(),
			[]mongo.IndexModel{
				{Keys: bson.D{{Key: "MAP.tx", Value: 1}, {Key: "timestamp", Value: -1}}, Options: options.Index().SetSparse(true)},
			},
		); err != nil {
			return nil, err
		}
		if _, err := db.Collection("follow").Indexes().CreateMany(
			context.Background(),
			[]mongo.IndexModel{
				{Keys: bson.D{{Key: "AIP.address", Value: 1}, {Key: "blk.i", Value: 1}}, Options: options.Index().SetSparse(true)},
				{Keys: bson.D{{Key: "MAP.idKey", Value: 1}, {Key: "blk.i", Value: 1}}, Options: options.Index().SetSparse(true)},
			},
		); err != nil {
			return nil, err
		}
		if _, err := db.Collection("unfollow").Indexes().CreateMany(
			context.Background(),
			[]mongo.IndexModel{
				{Keys: bson.D{{Key: "AIP.address", Value: 1}, {Key: "blk.i", Value: 1}}, Options: options.Index().SetSparse(true)},
				{Keys: bson.D{{Key: "MAP.idKey", Value: 1}, {Key: "blk.i", Value: 1}}, Options: options.Index().SetSparse(true)},
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

// func (l *LookupService) OutputAdded(ctx context.Context, outpoint *transaction.Outpoint, topic string, beef []byte) (err error) {
func (l *LookupService) OutputAdmittedByTopic(ctx context.Context, payload *engine.OutputAdmittedByTopic) error {
	_, tx, _, err := transaction.ParseBeef(payload.AtomicBEEF)
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
		return nil
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
func (l *LookupService) OutputSpent(ctx context.Context, payload *engine.OutputSpent) error {
	// Implementation for marking an output as spent
	return nil
}
func (l *LookupService) OutputNoLongerRetainedInHistory(ctx context.Context, outpoint *transaction.Outpoint, topic string) error {
	// Implementation for deleting an output event
	return nil
}
func (l *LookupService) OutputEvicted(ctx context.Context, outpoint *transaction.Outpoint) error {
	// Implementation for evicting an output
	return nil
}
func (l *LookupService) OutputBlockHeightUpdated(ctx context.Context, txid *chainhash.Hash, blockHeight uint32, blockIndex uint64) error {
	// Implementation for updating the block height of an output
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
				{Key: "text", Value: bson.D{
					{Key: "query", Value: query},                                // Use the input query, wrapped with wildcards
					{Key: "path", Value: bson.D{{Key: "wildcard", Value: "*"}}}, // Search across all fields
					// {Key: "allowAnalyzedField", Value: true},                    // Optional: Can improve performance if fields are analyzed
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

type FollowingResult struct {
	IDKey  string `json:"idKey"`
	Txid   string `json:"txid"`
	Height uint32 `json:"height"`
}

type FollowerResult struct {
	Address string `json:"address"`
	Txid    string `json:"txid"`
	Height  uint32 `json:"height"`
}

type followLookupDoc struct {
	ID  string `bson:"_id"`
	AIP []struct {
		Address string `bson:"address"`
	} `bson:"AIP"`
	MAP []bson.M `bson:"MAP"`
	Blk struct {
		I uint32 `bson:"i"`
	} `bson:"blk"`
	Timestamp int64 `bson:"timestamp"`
}

func (l *LookupService) FollowingByAddress(ctx context.Context, address string) ([]FollowingResult, error) {
	events, err := l.followEvents(ctx, bson.M{"AIP.address": address}, true)
	if err != nil {
		return nil, err
	}

	folded := FoldFollows(events)
	result := make([]FollowingResult, 0, len(folded))
	for _, event := range folded {
		result = append(result, FollowingResult{
			IDKey:  event.Key,
			Txid:   event.Txid,
			Height: event.Height,
		})
	}
	return result, nil
}

func (l *LookupService) FollowersByIDKey(ctx context.Context, idKey string) ([]FollowerResult, error) {
	events, err := l.followEvents(ctx, bson.M{"MAP.idKey": idKey}, false)
	if err != nil {
		return nil, err
	}

	folded := FoldFollows(events)
	result := make([]FollowerResult, 0, len(folded))
	for _, event := range folded {
		result = append(result, FollowerResult{
			Address: event.Key,
			Txid:    event.Txid,
			Height:  event.Height,
		})
	}
	return result, nil
}

func (l *LookupService) LikesByAddresses(ctx context.Context, addresses []string, limit int, offset int) ([]map[string]any, error) {
	cursor, err := l.db.Collection("like").Find(
		ctx,
		bson.M{"AIP.address": bson.M{"$in": addresses}},
		options.Find().
			SetSort(bson.D{{Key: "timestamp", Value: -1}}).
			SetLimit(int64(limit)).
			SetSkip(int64(offset)),
	)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []map[string]any
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	return docs, nil
}

func (l *LookupService) LikeCountsByTxIDs(ctx context.Context, txids []string) (map[string]int, error) {
	result := make(map[string]int, len(txids))
	for _, txid := range txids {
		result[txid] = 0
	}

	likes, err := l.aggregateReactionCounts(ctx, "like", "like", txids)
	if err != nil {
		return nil, err
	}
	unlikes, err := l.aggregateReactionCounts(ctx, "unlike", "unlike", txids)
	if err != nil {
		return nil, err
	}

	for txid, count := range likes {
		result[txid] = count
	}
	for txid, count := range unlikes {
		result[txid] -= count
		if result[txid] < 0 {
			result[txid] = 0
		}
	}

	return result, nil
}

func (l *LookupService) followEvents(ctx context.Context, filter bson.M, keyIsIDKey bool) ([]FollowEvent, error) {
	events := make([]FollowEvent, 0)
	for _, collection := range []struct {
		name      string
		eventType string
		unfollow  bool
	}{
		{name: "follow", eventType: "follow"},
		{name: "unfollow", eventType: "unfollow", unfollow: true},
	} {
		cursor, err := l.db.Collection(collection.name).Find(
			ctx,
			filter,
			options.Find().SetProjection(bson.M{
				"_id":       1,
				"AIP":       1,
				"MAP":       1,
				"blk.i":     1,
				"timestamp": 1,
			}),
		)
		if err != nil {
			return nil, err
		}

		for cursor.Next(ctx) {
			var doc followLookupDoc
			if err := cursor.Decode(&doc); err != nil {
				cursor.Close(ctx)
				return nil, err
			}
			mapEntry := mapEntryByType(doc.MAP, collection.eventType)
			if mapEntry == nil {
				continue
			}
			idKey := stringValue(mapEntry["idKey"])
			if idKey == "" {
				continue
			}
			address := firstAIPAddress(doc)
			if address == "" {
				continue
			}

			key := address
			if keyIsIDKey {
				key = idKey
			}
			events = append(events, FollowEvent{
				Key:       key,
				Txid:      doc.ID,
				Height:    doc.Blk.I,
				Timestamp: doc.Timestamp,
				Unfollow:  collection.unfollow,
			})
		}
		if err := cursor.Err(); err != nil {
			cursor.Close(ctx)
			return nil, err
		}
		cursor.Close(ctx)
	}
	return events, nil
}

func (l *LookupService) aggregateReactionCounts(ctx context.Context, collection string, eventType string, txids []string) (map[string]int, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"MAP.tx": bson.M{"$in": txids}}}},
		{{Key: "$unwind", Value: "$MAP"}},
		{{Key: "$match", Value: bson.M{"MAP.tx": bson.M{"$in": txids}, "MAP.type": eventType}}},
		{{Key: "$unwind", Value: "$AIP"}},
		{{Key: "$group", Value: bson.M{"_id": bson.M{"tx": "$MAP.tx", "addr": "$AIP.address"}}}},
		{{Key: "$group", Value: bson.M{"_id": "$_id.tx", "count": bson.M{"$sum": 1}}}},
	}

	cursor, err := l.db.Collection(collection).Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	rows := make([]struct {
		ID    string `bson:"_id"`
		Count int    `bson:"count"`
	}, 0)
	if err := cursor.All(ctx, &rows); err != nil {
		return nil, err
	}

	counts := make(map[string]int, len(rows))
	for _, row := range rows {
		counts[row.ID] = row.Count
	}
	return counts, nil
}

func mapEntryByType(entries []bson.M, eventType string) bson.M {
	for _, entry := range entries {
		if stringValue(entry["type"]) == eventType {
			return entry
		}
	}
	return nil
}

func firstAIPAddress(doc followLookupDoc) string {
	if len(doc.AIP) == 0 {
		return ""
	}
	return doc.AIP[0].Address
}

func stringValue(value any) string {
	if s, ok := value.(string); ok {
		return s
	}
	return ""
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
