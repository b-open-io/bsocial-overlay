// repair-map-binary audits legacy BSON without coercing its strings through UTF-8.
// It changes nothing unless --apply and a backup path are explicitly supplied.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/b-open-io/bsocial-overlay/bsocial"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"log"
	"os"
	"time"
)

func main() {
	apply := flag.Bool("apply", false, "Apply byte-preserving MAP conversions")
	backup := flag.String("backup", "", "New backup file for original projected BSON (required with apply)")
	limit := flag.Int64("limit", 10000, "Maximum documents to scan")
	after := flag.String("after", "", "Resume after this string document ID")
	collection := flag.String("collection", "message", "Collection to inspect")
	flag.Parse()
	if *limit < 1 || *limit > 100000 {
		log.Fatal("limit must be 1..100000")
	}
	if *collection != "message" && *collection != "post" && *collection != "like" && *collection != "friend" {
		log.Fatal("Unsupported collection")
	}
	var file *os.File
	var err error
	if *apply {
		if *backup == "" {
			log.Fatal("--backup is required")
		}
		file, err = os.OpenFile(*backup, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
	}
	uri := os.Getenv("MONGO_URL")
	if uri == "" {
		log.Fatal("MONGO_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(uri).SetMaxPoolSize(1))
	if err != nil {
		log.Fatal("Mongo connection failed")
	}
	defer client.Disconnect(context.Background())
	col := client.Database("bsocial").Collection(*collection)
	filter := bson.M{"MAP": bson.M{"$exists": true}}
	if *after != "" {
		filter["_id"] = bson.M{"$gt": *after}
	}
	cursor, err := col.Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1, "MAP": 1}).SetSort(bson.D{{Key: "_id", Value: 1}}).SetBatchSize(100).SetLimit(*limit))
	if err != nil {
		log.Fatal("Query failed")
	}
	defer cursor.Close(context.Background())
	scanned, affected, changed := 0, 0, int64(0)
	lastID := ""
	for cursor.Next(ctx) {
		raw := cursor.Current
		var doc bson.M
		if err = bson.Unmarshal(raw, &doc); err != nil {
			log.Fatal(err)
		}
		scanned++
		id, ok := doc["_id"].(string)
		if !ok {
			log.Fatal("Expected string transaction ID")
		}
		lastID = id
		safe, count := bsocial.PreserveLegacyMAPBinary(doc["MAP"])
		if count == 0 {
			continue
		}
		affected++
		fmt.Printf("invalid_binary txid=%s fields=%d\n", id, count)
		if !*apply {
			continue
		}
		if err = json.NewEncoder(file).Encode(map[string]string{"id": id, "bson": base64.StdEncoding.EncodeToString(raw)}); err != nil {
			log.Fatal(err)
		}
		if err = file.Sync(); err != nil {
			log.Fatal(err)
		}
		// Compare the original MAP field to avoid overwriting a concurrent reindex.
		result, err := col.UpdateOne(ctx, bson.M{"_id": id, "MAP": raw.Lookup("MAP")}, bson.M{"$set": bson.M{"MAP": safe}})
		if err != nil {
			log.Fatal(err)
		}
		changed += result.ModifiedCount
	}
	if err = cursor.Err(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("scanned=%d affected=%d changed=%d lastID=%s\n", scanned, affected, changed, lastID)
}
