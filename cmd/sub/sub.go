package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/GorillaPool/go-junglebus"
	"github.com/b-open-io/overlay/subscriber"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var TOPIC string
var FROM_BLOCK uint
var TAG string

func init() {
	godotenv.Load("../../.env")

	flag.StringVar(&TOPIC, "t", "", "Junglebus SubscriptionID")
	flag.StringVar(&TAG, "tag", "sub", "Queue name for Redis")
	flag.UintVar(&FROM_BLOCK, "s", 575000, "Start from block")
	flag.Parse()
}

func main() {
	ctx := context.Background()
	
	// Load environment configuration
	godotenv.Load("../../.env")
	
	// Set up Redis connection
	redisURL := os.Getenv("REDIS")
	if redisURL == "" {
		redisURL = "redis://localhost:6379"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	}
	redisClient := redis.NewClient(opts)
	
	// Set up JungleBus connection
	jungleBusURL := os.Getenv("JUNGLEBUS")
	if jungleBusURL == "" {
		log.Fatalf("JUNGLEBUS environment variable is required")
	}
	jbClient, err := junglebus.New(junglebus.WithHTTP(jungleBusURL))
	if err != nil {
		log.Fatalf("Failed to create JungleBus client: %v", err)
	}
	
	// Configure the subscriber
	subConfig := &subscriber.SubscriberConfig{
		TopicID:   TOPIC,
		QueueName: TAG,
		FromBlock: uint64(FROM_BLOCK),
		FromPage:  0,
		QueueSize: 10000000,
		LiteMode:  true,
	}
	
	// Create and start the subscriber
	sub := subscriber.NewSubscriber(subConfig, redisClient, jbClient)
	
	// Start subscription (blocks until context cancelled or signal received)
	if err := sub.Start(ctx); err != nil {
		log.Printf("Subscriber stopped: %v", err)
	}
}
