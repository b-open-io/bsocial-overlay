package main

import (
	"context"
	"database/sql"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/GorillaPool/go-junglebus"
	"github.com/GorillaPool/go-junglebus/models"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var JUNGLEBUS string
var QueueDb *sql.DB
var jb *junglebus.Client
var rdb *redis.Client
var TOPIC string
var FROM_BLOCK uint
var VERBOSE int
var TAG string

func init() {
	godotenv.Load("../../.env")

	flag.StringVar(&TOPIC, "t", "", "Junglebus SubscriptionID")
	flag.StringVar(&TAG, "tag", "sub", "Junglebus SubscriptionID")
	flag.UintVar(&FROM_BLOCK, "s", 575000, "Start from block")
	flag.IntVar(&VERBOSE, "v", 0, "Verbose")
	flag.Parse()

	if opts, err := redis.ParseURL(os.Getenv("REDIS")); err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	} else {
		rdb = redis.NewClient(opts)
	}
	JUNGLEBUS = os.Getenv("JUNGLEBUS")
	var err error
	if jb, err = junglebus.New(
		junglebus.WithHTTP(JUNGLEBUS),
	); err != nil {
		log.Fatalf("Failed to create Junglebus client: %v", err)
	}
}

func main() {
	ctx := context.Background()
	var sub *junglebus.Subscription
	txcount := 0
	var err error
	fromBlock := uint64(FROM_BLOCK)
	fromPage := uint64(0)
	if progress, err := rdb.HGet(ctx, "progress", TOPIC).Int(); err == nil {
		fromBlock = uint64(progress)
		log.Println("Resuming from block", fromBlock)
	}

	log.Println("Subscribing to Junglebus from block", fromBlock, fromPage)
	if sub, err = jb.SubscribeWithQueue(ctx,
		TOPIC,
		fromBlock,
		fromPage,
		junglebus.EventHandler{
			OnTransaction: func(txn *models.TransactionResponse) {
				txcount++
				log.Printf("[TX]: %d - %d: %d %s\n", txn.BlockHeight, txn.BlockIndex, len(txn.Transaction), txn.Id)
				if err := rdb.ZAdd(ctx, TAG, redis.Z{
					Member: txn.Id,
					Score:  float64(txn.BlockHeight)*1e9 + float64(txn.BlockIndex),
				}).Err(); err != nil {
					log.Panic(err)
				}
			},
			OnStatus: func(status *models.ControlResponse) {
				log.Printf("[STATUS]: %d %v %d processed\n", status.StatusCode, status.Message, txcount)
				switch status.StatusCode {
				case 200:
					if err := rdb.HSet(ctx, "progress", TOPIC, status.Block+1).Err(); err != nil {
						log.Panic(err)
					}
					txcount = 0
				case 999:
					log.Println(status.Message)
					log.Println("Unsubscribing...")
					sub.Unsubscribe()
					os.Exit(0)
					return
				}
			},
			OnError: func(err error) {
				log.Panicf("[ERROR]: %v\n", err)
			},
		},
		&junglebus.SubscribeOptions{
			QueueSize: 10000000,
			LiteMode:  true,
		},
	); err != nil {
		log.Panic(err)
	}
	defer func() {
		sub.Unsubscribe()
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigs:
	case <-ctx.Done():
	}

	rdb.Close()
}
