package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/GorillaPool/go-junglebus"
	"github.com/b-open-io/bsocial-overlay/bsocial"
	"github.com/b-open-io/overlay/beef"
	"github.com/b-open-io/overlay/publish"
	"github.com/b-open-io/overlay/storage"
	"github.com/b-open-io/overlay/subscriber"
	"github.com/bsv-blockchain/go-overlay-services/pkg/core/engine"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker/headers_client"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var CONCURRENCY int
var TOPIC string
var FROM_BLOCK uint
var QUEUE = "bsocial"
var chaintracker *headers_client.Client
var jb *junglebus.Client

type txSummary struct {
	tx  int
	out int
}

func init() {
	godotenv.Load("../../.env")
	chaintracker = &headers_client.Client{
		Url:    os.Getenv("BLOCK_HEADERS_URL"),
		ApiKey: os.Getenv("BLOCK_HEADERS_API_KEY"),
	}

	flag.StringVar(&TOPIC, "t", os.Getenv("TOPIC"), "Junglebus SubscriptionID")
	flag.UintVar(&FROM_BLOCK, "s", 575000, "Start from block")
	flag.IntVar(&CONCURRENCY, "c", 1, "Concurrency")
	flag.Parse()

	var err error
	if jb, err = junglebus.New(
		junglebus.WithHTTP(os.Getenv("JUNGLEBUS")),
	); err != nil {
		log.Fatalf("Failed to create Junglebus client: %v", err)
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
	// log.Println("Connecting to Redis", os.Getenv("REDIS"))
	if opts, err := redis.ParseURL(os.Getenv("REDIS")); err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	} else {
		rdb = redis.NewClient(opts)
	}
	// Initialize storage
	beefStore, err := beef.NewRedisBeefStorage(os.Getenv("REDIS_BEEF"), time.Hour*24*5)
	if err != nil {
		log.Fatalf("Failed to initialize tx storage: %v", err)
	}
	publisher, err := publish.NewRedisPublish(os.Getenv("REDIS"))
	if err != nil {
		log.Fatalf("Failed to initialize publisher: %v", err)
	}
	store, err := storage.NewMongoStorage(os.Getenv("MONGO_URL"), "bsocial", beefStore, publisher)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	log.Println("Storage initialized successfully")
	// defer store.Close()
	tm := "tm_bsocial"

	lookupService, err := bsocial.NewLookupService(
		os.Getenv("MONGO_URL"),
		"bsocial",
		publisher,
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
	}

	go func() {
		if TOPIC == "" {
			return
		}

		// Configure the subscriber
		subConfig := &subscriber.SubscriberConfig{
			TopicID:   TOPIC,
			QueueName: QUEUE,
			FromBlock: uint64(FROM_BLOCK),
			FromPage:  0,
			QueueSize: 1000,
			LiteMode:  true,
		}

		// Create and start the subscriber
		sub := subscriber.NewSubscriber(subConfig, rdb, jb)

		// Start subscription (will run until context cancelled)
		if err := sub.Start(ctx); err != nil {
			log.Printf("Subscriber stopped: %v", err)
			cancel()
		}
	}()

	done := make(chan *txSummary, 1000)
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
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

	client := &http.Client{Timeout: 30 * time.Second}
	retry := func(txid string) {
		// Negative scores preserve failed jobs in the same durable queue until due.
		if err := rdb.ZAdd(ctx, QUEUE, redis.Z{Member: txid, Score: -float64(time.Now().Add(time.Minute).UnixMilli())}).Err(); err != nil {
			log.Printf("Failed to defer %s: %v", txid, err)
		}
	}
	limiter := make(chan struct{}, CONCURRENCY)
	var wg sync.WaitGroup
	for {
		txids, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
			Key:     QUEUE,
			Stop:    "+inf",
			Start:   -float64(time.Now().UnixMilli()),
			Rev:     true,
			ByScore: true,
			Count:   int64(CONCURRENCY * 10),
		}).Result()
		if err != nil {
			log.Fatalf("Failed to query Redis: %v", err)
		}

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
					jobCtx, cancelJob := context.WithTimeout(ctx, 30*time.Second)
					defer cancelJob()
					if txid, err := chainhash.NewHashFromHex(txidStr); err != nil {
						log.Printf("Invalid queued txid %s: %v", txidStr, err)
						retry(txidStr)
					} else if beefBytes, err := bsocial.FetchBeef(jobCtx, client, os.Getenv("JUNGLEBUS"), txid); err != nil {
						log.Printf("Retrying %s after BEEF fetch: %v", txidStr, err)
						retry(txidStr)
					} else {
						taggedBeef := overlay.TaggedBEEF{
							Beef:   beefBytes,
							Topics: []string{tm},
						}
						if admit, err := e.Submit(jobCtx, taggedBeef, engine.SubmitModeHistorical, nil); err != nil {
							log.Printf("Retrying %s after submit: %v", txidStr, err)
							retry(txidStr)
						} else {
							if err := rdb.ZRem(ctx, QUEUE, txidStr).Err(); err != nil {
								log.Printf("Failed to acknowledge %s: %v", txidStr, err)
							}
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
		if len(txids) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}
