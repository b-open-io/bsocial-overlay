package main

import (
	"bufio"
	"context"
	"encoding/base64"
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
	"github.com/4chain-ag/go-overlay-services/pkg/server"
	"github.com/b-open-io/bsocial-overlay/bap"
	"github.com/b-open-io/bsocial-overlay/bsocial"
	"github.com/b-open-io/overlay/beef"
	"github.com/b-open-io/overlay/storage"

	// "github.com/b-open-io/overlay/util"
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

	beefStore, err := beef.NewMongoBeefStorage(os.Getenv("MONGO_URL"), "beef")
	if err != nil {
		log.Fatalf("Failed to initialize tx storage: %v", err)
	}

	bapStore, err := storage.NewMongoStorage(os.Getenv("MONGO_URL"), "bap", beefStore)
	if err != nil {
		log.Fatalf("Failed to initialize storage: %v", err)
	}
	// bapStorage := &bap.BAPStorage{MongoStorage: bapStore}
	bapLookup, err := bap.NewLookupService(
		os.Getenv("MONGO_URL"),
		"bap",
	)
	if err != nil {
		log.Fatalf("Failed to initialize lookup service: %v", err)
	}
	bsocialLookup, err := bsocial.NewLookupService(
		os.Getenv("MONGO_URL"),
		"bsocial",
	)
	if err != nil {
		log.Fatalf("Failed to initialize lookup service: %v", err)
	}
	// bapTm := "tm_bap"
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
		Storage:           bapStore,
		ChainTracker:      chaintracker,
		SyncConfiguration: map[string]engine.SyncConfiguration{
			// tm: {
			// 	Type:        engine.SyncConfigurationPeers,
			// 	Peers:       peers,
			// 	Concurrency: 1,
			// },
		},
		Broadcaster: &broadcaster.Arc{
			ApiUrl:  "https://arc.taal.com/v1",
			WaitFor: broadcaster.ACCEPTED_BY_NETWORK,
		},
		HostingURL:   os.Getenv("HOSTING_URL"),
		PanicOnError: true,
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

	httpServer, err := server.New(
		server.WithEngine(e),
		server.WithFiberMiddleware(logger.New()),
		server.WithConfig(&server.Config{
			Port: PORT,
		}),
	)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	httpServer.Router.Get("", func(c *fiber.Ctx) error {
		return c.SendString("Hello, World!")
	})

	// @Summary Validate identity by address
	// @Description Validates an identity at a specific block height or timestamp
	// @Tags identity
	// @Accept json
	// @Produce json
	// @Param request body IdentityValidByAddressParams true "Validation parameters including address, block height, and timestamp"
	// @Success 200 {object} Response{result=IdentityValidResponse} "Validation result with identity and profile data"
	// @Failure 400 {object} Response "Invalid request parameters"
	// @Failure 404 {object} Response "Identity not found"
	// @Failure 500 {object} Response "Server error"
	// @Router /identity/validByAddress [post]
	httpServer.Router.Post("/identity/validByAddress", func(c *fiber.Ctx) error {
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
			// } else if err := bapLookup.PopulateAddressHeights(c.Context(), id); err != nil {
			// 	return c.Status(fiber.StatusInternalServerError).JSON(Response{
			// 		Status:  "ERROR",
			// 		Message: err.Error(),
			// 	})
			// } else if profile, err := bapStorage.LoadProfile(c.Context(), id.BapId); err != nil {
			// 	return c.Status(fiber.StatusInternalServerError).JSON(Response{
			// 		Status:  "ERROR",
			// 		Message: err.Error(),
			// 	})
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

	httpServer.Router.Get("/person/:field/:bapId", func(c *fiber.Ctx) error {
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

		// // if bap ID
		// // if len(bapId) > 30 {
		// // 	// TODO: consider it an address, find match based on match on addresses field

		// // }

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

	// @Summary Get profiles with pagination
	// @Description Retrieves a paginated list of profiles
	// @Tags profile
	// @Accept json
	// @Produce json
	// @Param offset query integer false "Number of records to skip (default: 0)"
	// @Param limit query integer false "Number of records to return (default: 20, max: 100)"
	// @Success 200 {object} Response{result=[]map[string]interface{}} "List of profiles"
	// @Failure 400 {object} Response "Invalid pagination parameters"
	// @Failure 500 {object} Response "Server error"
	// @Router /profile [get]
	httpServer.Router.Get("/profile", func(c *fiber.Ctx) error {
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

	httpServer.Router.Get("/profile/:bapId", func(c *fiber.Ctx) error {
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

	// @Summary Get identity by ID
	// @Description Retrieves an identity by its unique identifier
	// @Tags identity
	// @Accept json
	// @Produce json
	// @Param idKey body string true "Identity key"
	// @Success 200 {object} Response{result=types.Identity} "Identity with profile data"
	// @Failure 404 {object} Response "Identity not found"
	// @Failure 500 {object} Response "Server error"
	// @Router /identity/get [post]
	httpServer.Router.Post("/identity/get", func(c *fiber.Ctx) error {
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
			// } else if err := bapStorage.PopulateAddressHeights(c.Context(), id); err != nil {
			// 	return c.Status(fiber.StatusInternalServerError).JSON(Response{
			// 		Status:  "ERROR",
			// 		Message: "Failed to populate address heights: " + err.Error(),
			// 	})
		} else {
			return c.JSON(Response{
				Status: "OK",
				Result: id,
			})
		}
	})

	httpServer.Router.Get("/subscribe/:topics", func(c *fiber.Ctx) error {
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
				// Broadcast the message to all clients subscribed to the topic
				if channels, exists := topicChannels[msg.Channel]; exists {
					for _, channel := range channels {
						channel <- msg
					}
				}

			case subReq := <-subscribe:
				// Add the client to the topicClients map
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

		// Gracefully shut down the Fiber app
		// if err := app.Shutdown(); err != nil {
		// 	log.Fatalf("Error shutting down server: %v", err)
		// }

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
			// for topic, syncConfig := range e.SyncConfiguration {
			// 	if syncConfig.Type == engine.SyncConfigurationPeers {
			// 		for _, peer := range syncConfig.Peers {
			// 			go func(peer string) {
			// 				req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/subscribe/%s", peer, topic))
			// 				res, err := http.DefaultClient.Do(req)
			// 				if err != nil {
			// 					// handle error
			// 				}
			// 				defer res.Body.Close() // don't forget!!

			// 				for ev, err := range sse.Read(res.Body, nil) {
			// 					if err != nil {
			// 						// handle read error
			// 						break // can end the loop as Read stops on first error anyway
			// 					}
			// 					// Do something with the events, parse the JSON or whatever.
			// 				}
			// 			}(peer)
			// 		}
			// 	}
			// }

		}()
	}
	// Start the server on the specified port
	<-httpServer.StartWithGracefulShutdown(ctx)

}

// Response represents the standard API response format
// @Description Standard API response wrapper
type Response struct {
	// Status of the response ("OK" or "ERROR")
	Status string `json:"status" example:"OK"`
	// Optional error message
	Message string `json:"message,omitempty" example:"Operation completed successfully"`
	// Response payload
	Result interface{} `json:"result,omitempty"`
}

// @Description Parameters for validating an attestation
type AttestationValidParams struct {
	// Blockchain address of the attestor
	Address string `json:"address" example:"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"`
	// Identity key
	IDKey string `json:"idKey" example:"3QxhyGy6ZE5SUpzXVb6AwnXYwH8g"`
	// Attribute being attested
	Attribute string `json:"attribute" example:"name"`
	// Value of the attestation
	Value string `json:"value" example:"John Doe"`
	// Nonce for uniqueness
	Nonce string `json:"nonce" example:"random123"`
	// URN identifier
	Urn string `json:"urn" example:"urn:bap:attestation:123"`
	// Hash of the attestation
	Hash string `json:"hash" example:"abc123def456"`
	// Block height for validation
	Block uint32 `json:"block" example:"123456"`
	// Timestamp for validation
	Timestamp uint32 `json:"timestamp" example:"1612137600"`
}

// IdentitiesRequest represents the request format for fetching multiple identities
// @Description Request format for retrieving multiple identities
type IdentitiesRequest struct {
	// List of identity keys to fetch
	IdKeys []string `json:"idKeys" example:"['id1', 'id2']"`
	// List of blockchain addresses to fetch identities for
	Addresses []string `json:"addresses" example:"['addr1', 'addr2']"`
}

// IdentityValidByAddressParams represents parameters for identity validation
// @Description Parameters for validating an identity at a specific point in time
type IdentityValidByAddressParams struct {
	// Blockchain address to validate
	Address string `json:"address" example:"1A1zP1eP5QGefi2DMPTfTL5SLmv7DivfNa"`
	// Block height to validate at (optional)
	Block uint32 `json:"block" example:"123456"`
	// Timestamp to validate at (optional)
	Timestamp uint32 `json:"timestamp" example:"1612137600"`
}

// ValidityRecord represents the validity status of an identity
// @Description Record indicating the validity of an identity at a point in time
type ValidityRecord struct {
	// Whether the identity is valid
	Valid bool `json:"valid" example:"true"`
	// Block height at which validity was checked
	Block uint32 `json:"block" example:"123456"`
	// Timestamp at which validity was checked
	Timestamp uint32 `json:"timestamp" example:"1612137600"`
}

// @Description Response for attestation validation
type AttestationValidResponse struct {
	// The attestation being validated
	bap.Attestation
	// Validity status record
	ValidityRecord
}

// IdentityValidResponse represents the response for identity validation
// @Description Response containing identity validation results
type IdentityValidResponse struct {
	// The identity being validated
	Identity bap.Identity `json:"identity"`
	// Validity status record
	ValidityRecord ValidityRecord `json:"validityRecord"`
	// Associated profile data if valid
	Profile interface{} `json:"profile,omitempty"`
}
