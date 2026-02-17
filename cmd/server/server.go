package main

// @title BSocial Overlay API
// @version 1.0
// @description Bitcoin SV Overlay Service for BAP (Bitcoin Attestation Protocol) identities and BSocial interactions. Provides real-time subscription capabilities and REST API endpoints for identity verification and social data lookup.
// @termsOfService https://your-domain.com/terms

// @contact.name API Support
// @contact.url https://your-domain.com/support
// @contact.email support@your-domain.com

// @license.name MIT
// @license.url https://opensource.org/licenses/MIT

// @host localhost:3000
// @BasePath /v1
// @schemes http https

// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-Auth-Token
// @description Authentication token using Bitcoin Signed Message (BSM)

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/b-open-io/bsocial-overlay/docs" // Import generated docs
	"github.com/b-open-io/bsocial-overlay/bap"
	"github.com/b-open-io/bsocial-overlay/bsocial"
	"github.com/b-open-io/overlay/beef"
	"github.com/b-open-io/overlay/publish"
	"github.com/b-open-io/overlay/storage"
	"github.com/bsv-blockchain/go-overlay-services/pkg/core/engine"
	"github.com/bsv-blockchain/go-overlay-services/pkg/server"
	"github.com/bsv-blockchain/go-sdk/transaction/broadcaster"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker/headers_client"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var chaintracker *headers_client.Client
var PORT int
var SYNC bool
var rdb, sub *redis.Client
var peers = []string{}
var e *engine.Engine
var currentBlock *headers_client.State

type subRequest struct {
	topics  []string
	msgChan chan *redis.Message
}

var subscribe = make(chan *subRequest, 100)   // Buffered channel
var unsubscribe = make(chan *subRequest, 100) // Buffered channel

func init() {
	log.Print("Initializing server...")
	godotenv.Load("../../.env")
	chaintracker = &headers_client.Client{
		Url:    os.Getenv("BLOCK_HEADERS_URL"),
		ApiKey: os.Getenv("BLOCK_HEADERS_API_KEY"),
	}
	PORT, _ = strconv.Atoi(os.Getenv("PORT"))
	flag.IntVar(&PORT, "p", PORT, "Port to listen on")
	flag.BoolVar(&SYNC, "s", false, "Start sync")
	flag.Parse()
	if PORT == 0 {
		PORT = 3000
	}
	if redisOpts, err := redis.ParseURL(os.Getenv("REDIS")); err != nil {
		log.Fatalf("Failed to parse Redis URL: %v", err)
	} else {
		rdb = redis.NewClient(redisOpts)
		sub = redis.NewClient(redisOpts)
	}
	PEERS := os.Getenv("PEERS")
	if PEERS != "" {
		peers = strings.Split(PEERS, ",")
	}
}

func main() {
	// Create a context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Channel to listen for OS signals
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt, syscall.SIGTERM)

	var cache *redis.Client
	if opts, err := redis.ParseURL(os.Getenv("REDIS")); err == nil {
		cache = redis.NewClient(opts)
	}

	beefStore, err := beef.NewRedisBeefStorage(os.Getenv("REDIS_BEEF"), time.Hour*24*5)
	if err != nil {
		log.Fatalf("Failed to initialize tx storage: %v", err)
	}

	publisher, err := publish.NewRedisPublish(os.Getenv("REDIS"))
	if err != nil {
		log.Fatalf("Failed to initialize publisher: %v", err)
	}

	bapStore, err := storage.NewMongoStorage(os.Getenv("MONGO_URL"), "bap", beefStore, publisher)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}

	bapLookup, err := bap.NewLookupService(
		os.Getenv("MONGO_URL"),
		"bap",
		publisher,
	)
	if err != nil {
		log.Fatalf("Failed to initialize lookup service: %v", err)
	}
	bsocialLookup, err := bsocial.NewLookupService(
		os.Getenv("MONGO_URL"),
		"bsocial",
		publisher,
	)
	if err != nil {
		log.Fatalf("Failed to initialize lookup service: %v", err)
	}

	e = &engine.Engine{
		Managers: map[string]engine.TopicManager{
			"tm_bap": &bap.TopicManager{
				Lookup: bapLookup,
			},
			"tm_bsocial": &bsocial.TopicManager{},
		},
		LookupServices: map[string]engine.LookupService{
			"ls_bap":     bapLookup,
			"ls_bsocial": bsocialLookup,
		},
		Storage:      bapStore,
		ChainTracker: chaintracker,
		SyncConfiguration: map[string]engine.SyncConfiguration{
			"tm_bap": {
				Type:        engine.SyncConfigurationPeers,
				Peers:       peers,
				Concurrency: 1,
			},
			"tm_bsocial": {
				Type:        engine.SyncConfigurationPeers,
				Peers:       peers,
				Concurrency: 16,
			},
		},
		Broadcaster: &broadcaster.Arc{
			ApiUrl:  "https://arc.taal.com/v1",
			WaitFor: broadcaster.ACCEPTED_BY_NETWORK,
		},
		HostingURL: os.Getenv("HOSTING_URL"),
	}

	if currentBlock, err = chaintracker.GetChaintip(ctx); err != nil {
		log.Println(err.Error())
	}

	go func() {
		ticker := time.NewTicker(time.Minute)
		for range ticker.C {
			if currentBlock, err = chaintracker.GetChaintip(ctx); err != nil {
				log.Println(err.Error())
			}
		}
	}()

	// Create our own fiber instance
	app := fiber.New()
	app.Use(logger.New())

	// Register overlay service routes
	server.RegisterRoutesWithErrorHandler(app, &server.RegisterRoutesConfig{
		ARCAPIKey:        os.Getenv("ARC_API_KEY"),
		ARCCallbackToken: os.Getenv("ARC_CALLBACK_TOKEN"),
		Engine:           e,
	})

	// Initialize handlers with dependencies
	getCurrentBlock := func() uint32 {
		if currentBlock != nil {
			return currentBlock.Height
		}
		return 0
	}
	handlers := NewHandlers(
		bapLookup,
		bsocialLookup,
		beefStore,
		e,
		cache,
		&getCurrentBlock,
		PORT,
		subscribe,
		unsubscribe,
	)

	// Register custom routes
	app.Get("/", handlers.ServeLandingPage)
	app.Post("/v1/ingest", handlers.IngestTransaction)
	app.Get("/v1/autofill", handlers.Autofill)
	app.Get("/v1/identity/search", handlers.SearchIdentities)
	app.Get("/v1/post/search", handlers.SearchPosts)
	app.Post("/v1/identity/validByAddress", handlers.ValidateIdentityByAddress)
	app.Post("/v1/identity/from-address", handlers.ValidateIdentityByAddress)
	app.Get("/v1/person/:field/:bapId", handlers.GetPersonImageField)
	app.Get("/v1/profile", handlers.ListProfiles)
	app.Get("/v1/profile/:bapId", handlers.GetProfileByBapId)
	app.Post("/v1/identity/get", handlers.GetIdentity)
	app.Get("/v1/subscribe/:topics", handlers.SubscribeToTopics)
	app.Get("/openapi.json", handlers.ServeOpenAPISpec)
	app.Get("/docs", handlers.ServeDocumentation)

	// Start the Redis PubSub goroutine
	go func() {
		pubSub := sub.PSubscribe(ctx, "*")
		pubSubChan := pubSub.Channel() // Subscribe to all topics
		defer pubSub.Close()

		topicChannels := make(map[string][]chan *redis.Message) // Map of topic to connected clients

		for {
			select {
			case <-ctx.Done():
				log.Println("Broadcasting stopped")
				return

			case msg := <-pubSubChan:
				// log.Println("Received message:", msg.Channel, msg.Payload)
				// Broadcast the message to all clients subscribed to the topic
				if channels, exists := topicChannels[msg.Channel]; exists {
					for _, channel := range channels {
						channel <- msg
					}
				}

			case subReq := <-subscribe:
				// log.Println("New subscription request:", subReq.topics)
				for _, topic := range subReq.topics {
					topicChannels[topic] = append(topicChannels[topic], subReq.msgChan)
				}

			case subReq := <-unsubscribe:
				// Remove the client from the topicClients map
				for _, topic := range subReq.topics {
					channels := topicChannels[topic]
					for i, c := range channels {
						if c == subReq.msgChan {
							topicChannels[topic] = append(channels[:i], channels[i+1:]...)
							break
						}
					}
				}
			}
		}
	}()

	// Goroutine to handle OS signals
	go func() {
		<-signalChan
		log.Println("Shutting down server...")

		// Cancel the context to stop goroutines
		cancel()

		// Gracefully shut down the server
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()

		if err := app.ShutdownWithContext(shutdownCtx); err != nil {
			log.Printf("Error shutting down server: %v", err)
		}

		// Close Redis connections
		if err := rdb.Close(); err != nil {
			log.Printf("Error closing Redis client: %v", err)
		}
		if err := sub.Close(); err != nil {
			log.Printf("Error closing Redis subscription client: %v", err)
		}

		log.Println("Server stopped.")
		os.Exit(0)
	}()

	if SYNC {
		go func() {
			if err := e.StartGASPSync(context.Background()); err != nil {
				log.Fatalf("Error starting sync: %v", err)
			}
			// peers := make(map[string][]string)
			// for topic, syncConfig := range e.SyncConfiguration {
			// 	if syncConfig.Type == engine.SyncConfigurationPeers {
			// 		for _, peer := range syncConfig.Peers {
			// 			if _, exists := peers[peer]; !exists {
			// 				peers[peer] = []string{}
			// 			}
			// 			peers[peer] = append(peers[peer], topic)
			// 		}
			// 	}
			// }

			// log.Println("Peers to subscribe to:", peers)

			// if len(peers) == 0 {
			// 	return
			// }
			// for peer, topics := range peers {
			// 	go func(peer string) {
			// 		for {
			// 			start := time.Now()
			// 			url := fmt.Sprintf("%s/subscribe/%s", peer, strings.Join(topics, ","))
			// 			log.Println("Subscribing to peer:", url)
			// 			res, err := http.Get(fmt.Sprintf("%s/subscribe/%s", peer, strings.Join(topics, ",")))
			// 			if err != nil {
			// 				log.Println("Error subscribing to peer:", err)
			// 				return
			// 			}
			// 			defer res.Body.Close()

			// 			for ev, err := range sse.Read(res.Body, nil) {
			// 				if err != nil {
			// 					// handle read error
			// 					break
			// 				}
			// 				taggedBeef := overlay.TaggedBEEF{
			// 					Topics: []string{ev.Type},
			// 				}
			// 				if taggedBeef.Beef, err = base64.StdEncoding.DecodeString(ev.Data); err != nil {
			// 					log.Println("Error decoding base64:", err)
			// 				} else if _, _, txid, err := transaction.ParseBeef(taggedBeef.Beef); err != nil {
			// 					log.Println("Error parsing BEEF:", err)
			// 				} else if steak, err := e.Submit(ctx, taggedBeef, engine.SubmitModeHistorical, nil); err != nil {
			// 					log.Println("Error submitting tagged BEEF:", err)
			// 				} else {
			// 					log.Println("Successfully submitted tagged BEEF:", txid.String(), steak[ev.Type].OutputsToAdmit)
			// 				}
			// 			}
			// 			res.Body.Close()
			// 			duration := time.Since(start)
			// 			if duration < 5*time.Second {
			// 				time.Sleep(5*time.Second - duration)
			// 			}
			// 		}
			// 	}(peer)
			// }
		}()
	}

	// Start the server
	log.Printf("Starting server on port %d...", PORT)
	if err := app.Listen(fmt.Sprintf(":%d", PORT)); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// Response represents the standard API response format
type Response struct {
	Status  string      `json:"status" example:"OK"`
	Message string      `json:"message,omitempty" example:"Operation completed successfully"`
	Result  interface{} `json:"result,omitempty"`
}

type AttestationValidParams struct {
	Address   string `json:"address" example:"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"`
	IDKey     string `json:"idKey" example:"3QxhyGy6ZE5SUpzXVb6AwnXYwH8g"`
	Attribute string `json:"attribute" example:"name"`
	Value     string `json:"value" example:"John Doe"`
	Nonce     string `json:"nonce" example:"random123"`
	Urn       string `json:"urn" example:"urn:bap:attestation:123"`
	Hash      string `json:"hash" example:"abc123def456"`
	Block     uint32 `json:"block" example:"123456"`
	Timestamp uint32 `json:"timestamp" example:"1612137600"`
}

type IdentitiesRequest struct {
	IdKeys    []string `json:"idKeys" example:"['id1', 'id2']"`
	Addresses []string `json:"addresses" example:"['addr1', 'addr2']"`
}

type IdentityValidByAddressParams struct {
	Address   string `json:"address" example:"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"`
	Block     uint32 `json:"block" example:"123456"`
	Timestamp uint32 `json:"timestamp" example:"1612137600"`
}

type ValidityRecord struct {
	Valid     bool   `json:"valid" example:"true"`
	Block     uint32 `json:"block" example:"123456"`
	Timestamp uint32 `json:"timestamp" example:"1612137600"`
}

type AttestationValidResponse struct {
	bap.Attestation
	ValidityRecord
}

type IdentityValidResponse struct {
	Identity       bap.Identity   `json:"identity"`
	ValidityRecord ValidityRecord `json:"validityRecord"`
	Profile        interface{}    `json:"profile,omitempty"`
}
