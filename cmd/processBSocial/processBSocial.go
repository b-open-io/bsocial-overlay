package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/4chain-ag/go-overlay-services/pkg/core/engine"
	"github.com/b-open-io/bsocial-overlay/bsocial"
	"github.com/b-open-io/overlay/storage"
	"github.com/b-open-io/overlay/util"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker/headers_client"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var CONCURRENCY = 16
var chaintracker headers_client.Client

type txSummary struct {
	tx  int
	out int
}

func init() {
	godotenv.Load("../../.env")
	chaintracker = headers_client.Client{
		Url:    os.Getenv("BLOCK_HEADERS_URL"),
		ApiKey: os.Getenv("BLOCK_HEADERS_API_KEY"),
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS signals for graceful shutdown
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signalChan
		log.Println("Received shutdown signal, cleaning up...")
		cancel()
	}()

	var rdb *redis.Client
	log.Println("Connecting to Redis", os.Getenv("REDIS"))
	if opts, err := redis.ParseURL(os.Getenv("REDIS")); err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	} else {
		rdb = redis.NewClient(opts)
	}
	// Initialize storage
	txStore, err := util.NewRedisTxStorage(os.Getenv("REDIS_BEEF"))
	if err != nil {
		log.Fatalf("Failed to initialize tx storage: %v", err)
	}
	store, err := storage.NewMongoStorage(os.Getenv("MONGO_URL"), "bsocial")
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	// defer store.Close()
	tm := "tm_bsocial"

	lookupService, err := bsocial.NewLookupService(
		os.Getenv("MONGO_URL"),
	)
	if err != nil {
		log.Fatalf("Failed to initialize lookup service: %v", err)
	}
	e := engine.Engine{
		Managers: map[string]engine.TopicManager{
			tm: &bsocial.TopicManager{},
		},
		LookupServices: map[string]engine.LookupService{
			"ls_bsocial": lookupService,
		},
		Storage:      store,
		ChainTracker: chaintracker,
		PanicOnError: true,
	}

	done := make(chan *txSummary, 1000)
	go func() {
		ticker := time.NewTicker(time.Minute)
		txcount := 0
		outcount := 0
		// accTime
		lastTime := time.Now()
		for {
			select {
			case summary := <-done:
				txcount += summary.tx
				outcount += summary.out
				// log.Println("Got done")

			case <-ticker.C:
				log.Printf("Processed tx %d o %d in %v %vtx/s\n", txcount, outcount, time.Since(lastTime), float64(txcount)/time.Since(lastTime).Seconds())
				lastTime = time.Now()
				txcount = 0
				outcount = 0
			case <-ctx.Done():
				log.Println("Context canceled, stopping processing...")
				return
			}
		}
	}()

	txids, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     "bsocial",
		Stop:    "+inf",
		Start:   "-inf",
		ByScore: true,
	}).Result()
	if err != nil {
		log.Fatalf("Failed to query Redis: %v", err)
	}

	limiter := make(chan struct{}, CONCURRENCY) // Limit concurrent processing to 100 transactions
	var wg sync.WaitGroup
	for _, txidStr := range txids {
		select {
		case <-ctx.Done():
			log.Println("Context canceled, stopping processing...")
			return
		default:
			wg.Add(1)
			limiter <- struct{}{} // Acquire a slot in the limiter
			go func(txidStr string) {
				defer wg.Done()
				defer func() { <-limiter }() // Release the slot in the limiter
				if txid, err := chainhash.NewHashFromHex(txidStr); err != nil {
					log.Fatalf("Invalid txid: %v", err)
				} else if tx, err := txStore.LoadTx(ctx, txid); err != nil {
					log.Fatalf("Failed to load transaction: %v", err)
				} else {
					beef := &transaction.Beef{
						Version:      transaction.BEEF_V2,
						Transactions: map[string]*transaction.BeefTx{},
					}
					for _, input := range tx.Inputs {
						if input.SourceTransaction, err = txStore.LoadTx(ctx, input.SourceTXID); err != nil {
							log.Fatalf("Failed to load source transaction: %v", err)
						} else if _, err := beef.MergeTransaction(input.SourceTransaction); err != nil {
							log.Fatalf("Failed to merge source transaction: %v", err)
						}
					}
					if _, err := beef.MergeTransaction(tx); err != nil {
						log.Fatalf("Failed to merge source transaction: %v", err)
					}

					taggedBeef := overlay.TaggedBEEF{
						Topics: []string{tm},
					}
					logTime := time.Now()
					if taggedBeef.Beef, err = beef.AtomicBytes(txid); err != nil {
						log.Fatalf("Failed to generate BEEF: %v", err)
					} else if admit, err := e.Submit(ctx, taggedBeef, engine.SubmitModeHistorical, nil); err != nil {
						log.Fatalf("Failed to submit transaction: %v", err)
					} else {
						if err := rdb.ZRem(ctx, "bsocial", txidStr).Err(); err != nil {
							log.Fatalf("Failed to delete from queue: %v", err)
						}
						log.Println("Processed", txid, "in", time.Since(logTime), "as", admit[tm].OutputsToAdmit)
						done <- &txSummary{
							tx:  1,
							out: len(admit[tm].OutputsToAdmit),
						}
					}
				}
			}(txidStr)
		}
	}
	wg.Wait()

	// Close the database connection
	// sub.QueueDb.Close()
	log.Println("Application shutdown complete.")
}
