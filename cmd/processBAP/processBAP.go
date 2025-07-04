package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/4chain-ag/go-overlay-services/pkg/core/engine"
	"github.com/GorillaPool/go-junglebus"
	"github.com/b-open-io/bsocial-overlay/bap"
	"github.com/b-open-io/overlay/beef"
	"github.com/b-open-io/overlay/publish"
	"github.com/b-open-io/overlay/storage"
	"github.com/b-open-io/overlay/subscriber"
	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker/headers_client"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var TOPIC string
var FROM_BLOCK uint
var QUEUE = "bap"
var chaintracker headers_client.Client
var jb *junglebus.Client

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

	flag.StringVar(&TOPIC, "t", os.Getenv("TOPIC"), "Junglebus SubscriptionID")
	flag.UintVar(&FROM_BLOCK, "s", 575000, "Start from block")
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
	store, err := storage.NewMongoStorage(os.Getenv("MONGO_URL"), "bap", beefStore, publisher)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	tm := "tm_bap"

	lookupService, err := bap.NewLookupService(
		os.Getenv("MONGO_URL"),
		"bap",
		publisher,
	)
	if err != nil {
		log.Fatalf("Failed to initialize lookup service: %v", err)
	}
	e := engine.Engine{
		Managers: map[string]engine.TopicManager{
			tm: &bap.TopicManager{
				Lookup: lookupService,
			},
		},
		LookupServices: map[string]engine.LookupService{
			"ls_bap": lookupService,
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
			QueueSize: 10000000,
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

	log.Println("Starting transaction processing...")
	for {
		txids, err := rdb.ZRangeArgs(ctx, redis.ZRangeArgs{
			Key:     QUEUE,
			Stop:    "+inf",
			Start:   "-inf",
			ByScore: true,
		}).Result()
		if err != nil {
			log.Fatalf("Failed to query Redis: %v", err)
		}
		log.Println("Found", len(txids), "transactions in queue")

		for _, txidStr := range txids {
			select {
			case <-ctx.Done():
				log.Println("Context canceled, stopping processing...")
				return
			default:
				startTime := time.Now()
				if txid, err := chainhash.NewHashFromHex(txidStr); err != nil {
					log.Fatalf("Invalid txid: %v", err)
				} else if _, err := beefStore.LoadTx(ctx, txid, &chaintracker); err != nil {
					log.Fatalf("Failed to load transaction: %v", err)
				} else if beefBytes, err := beefStore.LoadBeef(ctx, txid); err != nil {
					log.Fatalf("Failed to load BEEF: %v", err)
				} else {
					taggedBeef := overlay.TaggedBEEF{
						Topics: []string{tm},
						Beef:   beefBytes,
					}
					log.Println(txidStr, "Loaded in", time.Since(startTime))
					if admit, err := e.Submit(ctx, taggedBeef, engine.SubmitModeHistorical, nil); err != nil {
						log.Fatalf("Failed to submit transaction: %v", err)
					} else {
						if err := rdb.ZRem(ctx, QUEUE, txidStr).Err(); err != nil {
							log.Fatalf("Failed to delete from queue: %v", err)
						}
						log.Println("Processed", txid, "in", time.Since(startTime), "as", admit[tm].OutputsToAdmit)
						done <- &txSummary{
							tx:  1,
							out: len(admit[tm].OutputsToAdmit),
						}
					}
				}
			}
		}
		if len(txids) == 0 {
			log.Println("No transactions to process, waiting for 10 seconds...")
			time.Sleep(10 * time.Second)
		}
	}
}
