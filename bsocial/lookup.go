package bsocial

import (
	"context"
	"log"
	"strings"
	"unicode/utf8"

	"github.com/bitcoinschema/go-bmap"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/overlay/lookup"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type LookupService struct {
	db *mongo.Database
}

func NewLookupService(connString string, dbName string) (*LookupService, error) {
	clientOptions := options.Client().ApplyURI(connString).SetMaxPoolSize(100)
	if client, err := mongo.Connect(context.Background(), clientOptions); err != nil {
		return nil, err
	} else {
		return &LookupService{
			db: client.Database(dbName),
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
		bson.M{"$set": bsonData},
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

	if bmapData.AIP != nil {
		bsonData["AIP"] = bmapData.AIP
	}

	if bmapData.AIP != nil {
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

	if len(bmapData.B) > 0 {
		b := map[string]any{
			"content-type": bmapData.B[0].MediaType,
			"encoding":     bmapData.B[0].Encoding,
			"filename":     bmapData.B[0].Filename,
		}

		if strings.HasPrefix(bmapData.B[0].MediaType, "text") {
			if len(bmapData.B[0].Data) > 256*1024 {
				b["content"] = string(bmapData.B[0].Data[:256*1024])
			} else {
				b["content"] = string(bmapData.B[0].Data)
			}
		}

		bsonData["B"] = b
	}

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
