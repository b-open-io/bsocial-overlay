package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/4chain-ag/go-overlay-services/pkg/core/engine"
	"github.com/4chain-ag/go-overlay-services/pkg/server2"
	"github.com/b-open-io/bsocial-overlay/bap"
	"github.com/b-open-io/bsocial-overlay/bsocial"
	"github.com/b-open-io/overlay/beef"
	"github.com/b-open-io/overlay/publish"
	"github.com/b-open-io/overlay/storage"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/broadcaster"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker/headers_client"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

var chaintracker headers_client.Client
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
	chaintracker = headers_client.Client{
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

	// Create server2 instance with configuration
	httpServer := server2.New(
		server2.WithEngine(e),
		server2.WithMiddleware(logger.New()),
		server2.WithConfig(server2.Config{
			Port:                  PORT,
			OctetStreamLimit:      server2.DefaultConfig.OctetStreamLimit,
			ConnectionReadTimeout: server2.DefaultConfig.ConnectionReadTimeout,
			ARCAPIKey:             os.Getenv("ARC_API_KEY"),
			ARCCallbackToken:      os.Getenv("ARC_CALLBACK_TOKEN"),
		}),
	)

	// Register custom routes
	httpServer.RegisterRoute("POST", "/api/v1/ingest", func(c *fiber.Ctx) error {
		if tx, err := transaction.NewTransactionFromBytes(c.Body()); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(Response{
				Status:  "ERROR",
				Message: "Invalid transaction data: " + err.Error(),
			})
		} else {
			for _, input := range tx.Inputs {
				if sourceBeef, err := beefStore.LoadBeef(c.Context(), input.SourceTXID); err != nil {
					return c.Status(fiber.StatusNotFound).JSON(Response{
						Status:  "ERROR",
						Message: "Failed to load input transaction: " + err.Error(),
					})
				} else if input.SourceTransaction, err = transaction.NewTransactionFromBEEF(sourceBeef); err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(Response{
						Status:  "ERROR",
						Message: "Failed to parse input transaction: " + err.Error(),
					})
				}
			}
			taggedBeef := overlay.TaggedBEEF{
				Topics: []string{"tm_bap", "tm_bsocial"},
			}

			if taggedBeef.Beef, err = tx.AtomicBEEF(false); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(Response{
					Status:  "ERROR",
					Message: "Failed to generate BEEF: " + err.Error(),
				})
			} else if _, err := e.Submit(ctx, taggedBeef, engine.SubmitModeHistorical, nil); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(Response{
					Status:  "ERROR",
					Message: "Failed to submit transaction: " + err.Error(),
				})
			} else {
				return c.JSON(Response{
					Status: "OK",
					Result: map[string]any{
						"txid": tx.TxID().String(),
					},
				})
			}
		}
	})

	httpServer.RegisterRoute("GET", "/api/v1/autofill", func(c *fiber.Ctx) error {
		q := c.Query("q")

		if cache != nil {
			if cached, err := cache.HGet(c.Context(), "autofill", q).Result(); err != nil {
				log.Println("Cache error:", err)
			} else if cached != "" {
				return c.JSON(Response{
					Status: "OK",
					Result: json.RawMessage(cached),
				})
			}
		}

		if identities, err := bapLookup.Search(c.Context(), q, 3, 0); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to search identities: " + err.Error(),
			})
		} else if posts, err := bsocialLookup.Search(c.Context(), q, 10, 0); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to search posts: " + err.Error(),
			})
		} else {

			if result, err := json.Marshal(map[string]any{
				"identities": identities,
				"posts":      posts,
			}); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(Response{
					Status:  "ERROR",
					Message: "Failed to marshal result: " + err.Error(),
				})
			} else {
				if cache != nil {
					cache.Pipelined(c.Context(), func(pipe redis.Pipeliner) error {
						pipe.HSet(c.Context(), "autofill", q, result)
						pipe.HExpire(c.Context(), "autofill", 15*time.Minute, q)
						return nil
					})
				}
				return c.JSON(Response{
					Status: "OK",
					Result: json.RawMessage(result),
				})
			}
		}
	})

	httpServer.RegisterRoute("GET", "/api/v1/identity/search", func(c *fiber.Ctx) error {
		q := c.Query("q")
		limit := c.QueryInt("limit", 20)  // Default limit is 20
		offset := c.QueryInt("offset", 0) // Default offset is 0
		if identities, err := bapLookup.Search(c.Context(), q, limit, offset); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to search identities: " + err.Error(),
			})
		} else {
			// Return the list of identities
			return c.JSON(Response{
				Status: "OK",
				Result: identities,
			})
		}
	})

	httpServer.RegisterRoute("GET", "/api/v1/post/search", func(c *fiber.Ctx) error {
		q := c.Query("q")
		limit := c.QueryInt("limit", 20)  // Default limit is 20
		offset := c.QueryInt("offset", 0) // Default offset is 0
		if posts, err := bsocialLookup.Search(c.Context(), q, limit, offset); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to search identities: " + err.Error(),
			})
		} else {
			// Return the list of identities
			return c.JSON(Response{
				Status: "OK",
				Result: posts,
			})
		}
	})

	httpServer.RegisterRoute("POST", "/api/v1/identity/validByAddress", func(c *fiber.Ctx) error {
		req := &IdentityValidByAddressParams{}
		c.BodyParser(&req)
		if req.Block == 0 && req.Timestamp == 0 {
			req.Block = currentBlock.Height
			req.Timestamp = currentBlock.Header.Timestamp
		}

		if id, err := bapLookup.LoadIdentityByAddress(c.Context(), req.Address); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: err.Error(),
			})
		} else if id == nil {
			return c.Status(fiber.StatusNotFound).JSON(Response{
				Status:  "ERROR",
				Message: fmt.Sprintf("Identity not found for address %s", req.Address),
			})
		} else if req.Block > 0 {
			currentAddress := ""
			for _, addr := range id.Addresses {
				if addr.Block <= req.Block {
					currentAddress = addr.Address
				} else {
					break
				}
			}
			if currentAddress != req.Address {
				return c.JSON(Response{
					Status: "OK",
					Result: IdentityValidResponse{
						Identity: *id,
						ValidityRecord: ValidityRecord{
							Valid:     false,
							Block:     req.Block,
							Timestamp: req.Timestamp,
						},
					},
				})
			} else {
				return c.JSON(Response{
					Status: "OK",
					Result: IdentityValidResponse{
						Identity: *id,
						ValidityRecord: ValidityRecord{
							Valid:     true,
							Block:     req.Block,
							Timestamp: req.Timestamp,
						},
						Profile: id.Profile,
					},
				})
			}
		} else {
			currentAddress := ""
			for _, addr := range id.Addresses {
				if addr.Timestamp <= req.Timestamp {
					currentAddress = addr.Address
				} else {
					break
				}
			}
			if currentAddress != req.Address {
				return c.JSON(Response{
					Status: "OK",
					Result: IdentityValidResponse{
						Identity: *id,
						ValidityRecord: ValidityRecord{
							Valid:     false,
							Block:     req.Block,
							Timestamp: req.Timestamp,
						},
					},
				})
			} else {
				return c.JSON(Response{
					Status: "OK",
					Result: IdentityValidResponse{
						Identity: *id,
						ValidityRecord: ValidityRecord{
							Valid:     true,
							Block:     req.Block,
							Timestamp: req.Timestamp,
						},
						Profile: id.Profile,
					},
				})
			}
		}
	})

	httpServer.RegisterRoute("GET", "/api/v1/person/:field/:bapId", func(c *fiber.Ctx) error {
		field := c.Params("field")
		if field == "" {
			return c.Status(fiber.StatusBadRequest).JSON(Response{
				Status:  "ERROR",
				Message: "Field is required",
			})
		}

		bapId := c.Params("bapId")
		if bapId == "" {
			return c.Status(fiber.StatusBadRequest).JSON(Response{
				Status:  "ERROR",
				Message: "BAPID is required",
			})
		}
		var data map[string]any
		if id, err := bapLookup.LoadIdentityById(c.Context(), bapId); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: err.Error(),
			})
		} else if id == nil || id.Profile == nil {
			return c.Status(fiber.StatusNotFound).JSON(Response{
				Status:  "ERROR",
				Message: fmt.Sprintf("Profile not found for BAPID %s", bapId),
			})
		} else {
			data = id.Profile
		}

		imageUrl, imageExists := data[field].(string)
		if !imageExists || strings.TrimSpace(imageUrl) == "" {
			// return the default image url
			imageUrl = "/096b5fdcb6e88f8f0325097acca2784eabd62cd4d1e692946695060aff3d6833_7"
		}

		// Check if the imageUrl is a raw txid (64 character hex string)
		if len(imageUrl) == 64 && !strings.HasPrefix(imageUrl, "/") && !strings.HasPrefix(imageUrl, "http") && !strings.HasPrefix(imageUrl, "data:") {
			imageUrl = "/" + imageUrl
		}

		if strings.HasPrefix(imageUrl, "data:") {
			// Handle base64-encoded data URL
			commaIndex := strings.Index(imageUrl, ",")
			if commaIndex < 0 {
				return c.Status(fiber.StatusBadRequest).JSON(Response{
					Status:  "ERROR",
					Message: "Invalid data URL format",
				})
			}

			// Extract the metadata and data
			// Remove "data:" prefix from metaData
			metaData := strings.TrimPrefix(imageUrl[:commaIndex], "data:")
			// metadata = image/jpeg;base64
			metaDataParts := strings.Split(metaData, ";")

			metaData = metaDataParts[0]
			// metadata = image/jpeg

			base64Data := imageUrl[commaIndex+1:]

			// Parse the media type from the metadata
			mediaType, _, err := mime.ParseMediaType(metaData)
			if err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(Response{
					Status:  "ERROR",
					Message: "Invalid media type in data URL " + metaData + " " + err.Error(),
				})
			}

			// image/jpeg;base64
			log.Println(("Data URL: " + base64Data))

			// Decode the base64 data
			imgData, err := base64.StdEncoding.DecodeString(base64Data)
			if err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(Response{
					Status:  "ERROR",
					Message: "Failed to decode base64 image data",
				})
			}

			// Set the Content-Type header
			c.Set("Content-Type", mediaType)

			// Return the image data
			return c.Send(imgData)
		} else {
			// Handle regular image URL
			// If the image URL uses a custom protocol (e.g., bitfs://), handle it accordingly
			// Handle regular image URL
			if strings.HasPrefix(imageUrl, "bitfs://") {
				// Convert bitfs://<txid>.out.<vout>.<script_chunk> to https://ordfs.network/<txid>_<vout>
				baseUrl := "https://ordfs.network/"
				// Remove the "bitfs://" prefix
				path := strings.TrimPrefix(imageUrl, "bitfs://")
				// Split the path by "."
				parts := strings.Split(path, ".")
				if len(parts) >= 3 && parts[1] == "out" {
					txid := parts[0]
					// vout := parts[2]
					// Construct the new URL
					imageUrl = baseUrl + txid // + "_" + vout
				} else {
					// Handle error: unexpected format
					return c.Status(fiber.StatusBadRequest).JSON(Response{
						Status:  "ERROR",
						Message: "Invalid bitfs URL format",
					})
				}
			}

			// Fetch the image data from the URL
			// if imageUrl.startsWith
			if strings.HasPrefix(imageUrl, "/") {
				imageUrl = "https://ordfs.network" + imageUrl
			}

			resp, err := http.Get(imageUrl)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(Response{
					Status:  "ERROR",
					Message: "Failed to fetch image at " + imageUrl + err.Error(),
				})
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return c.Status(fiber.StatusNotFound).JSON(Response{
					Status:  "ERROR",
					Message: "Image not found at the specified URL",
				})
			}

			// Read the image data
			imgData, err := io.ReadAll(resp.Body)
			if err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(Response{
					Status:  "ERROR",
					Message: "Failed to read image data",
				})
			}

			// Determine the content type
			contentType := resp.Header.Get("Content-Type")
			if contentType == "" {
				// Fallback to detecting content type from data
				contentType = http.DetectContentType(imgData)
			}

			// Set the appropriate content type header
			c.Set("Content-Type", contentType)

			// Return the image data as the response
			return c.Send(imgData)
		}
	})

	httpServer.RegisterRoute("GET", "/api/v1/profile", func(c *fiber.Ctx) error {
		// Default pagination parameters
		offset := c.QueryInt("offset", 0) // Default offset is 0
		limit := c.QueryInt("limit", 20)  // Set a default limit

		if profiles, err := bapLookup.LoadProfiles(c.Context(), limit, offset); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to fetch profiles: " + err.Error(),
			})
		} else {
			// Return the list of profiles
			return c.JSON(Response{
				Status: "OK",
				Result: profiles,
			})
		}
	})

	httpServer.RegisterRoute("GET", "/api/v1/profile/:bapId", func(c *fiber.Ctx) error {
		bapId := c.Params("bapId")
		if identity, err := bapLookup.LoadIdentityById(c.Context(), bapId); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to fetch profiles: " + err.Error(),
			})
		} else {
			// Return the list of profiles
			return c.JSON(Response{
				Status: "OK",
				Result: identity.Profile,
			})
		}
	})

	httpServer.RegisterRoute("POST", "/api/v1/identity/get", func(c *fiber.Ctx) error {
		req := map[string]string{}
		if err := c.BodyParser(&req); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(Response{
				Status:  "ERROR",
				Message: "Invalid request body: " + err.Error(),
			})
		} else if id, err := bapLookup.LoadIdentityById(c.Context(), req["idKey"]); err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to fetch identity: " + err.Error(),
			})
		} else if id == nil {
			return c.Status(fiber.StatusNotFound).JSON(Response{
				Status:  "ERROR",
				Message: "Identity not found for ID key: " + req["idKey"],
			})
		} else {
			return c.JSON(Response{
				Status: "OK",
				Result: id,
			})
		}
	})

	httpServer.RegisterRoute("GET", "/api/v1/subscribe/:topics", func(c *fiber.Ctx) error {
		topicsParam := c.Params("topics")
		if topicsParam == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
				"error": "Missing topics",
			})
		}
		topics := strings.Split(topicsParam, ",")
		if len(topics) == 0 {
			return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
				"error": "No topics provided",
			})
		}

		// Set headers for SSE
		c.Set("Content-Type", "text/event-stream")
		c.Set("Cache-Control", "no-cache")
		c.Set("Connection", "keep-alive")
		c.Set("Transfer-Encoding", "chunked")

		// Add the client to the topicClients map
		subReq := &subRequest{
			topics:  topics,
			msgChan: make(chan *redis.Message, 25),
		}
		subscribe <- subReq

		// Create a channel to detect when connection is closed
		disconnected := make(chan struct{})

		ctx := c.Context()
		ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
			fmt.Println("Client connected for topics:", topics)

			// Start a goroutine to detect when connection is closed
			go func() {
				<-ctx.Done()
				fmt.Println("Context done, closing connection for topics:", topics)
				unsubscribe <- subReq
				close(disconnected)
			}()

			// Keep writing messages until disconnection
			for {
				select {
				case <-disconnected:
					return
				case msg := <-subReq.msgChan:
					fmt.Fprintf(w, "id: %d\n", time.Now().UnixNano())
					fmt.Fprintf(w, "event: %s\n", msg.Channel)
					fmt.Fprintf(w, "data: %s\n\n", msg.Payload)

					if err := w.Flush(); err != nil {
						fmt.Printf("Error while flushing: %v. Closing connection.\n", err)
						unsubscribe <- subReq
						return
					}
				}
			}
		})

		// Critical: Return nil but DON'T execute any code after the SetBodyStreamWriter
		return nil
	})

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

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
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
	if err := httpServer.ListenAndServe(ctx); err != nil {
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
