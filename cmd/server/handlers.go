package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/b-open-io/bsocial-overlay/bap"
	"github.com/b-open-io/bsocial-overlay/bsocial"
	"github.com/b-open-io/overlay/beef"
	"github.com/bsv-blockchain/go-overlay-services/pkg/core/engine"
	"github.com/bsv-blockchain/go-sdk/overlay"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/swaggo/swag/v2"
	scalar "github.com/bdpiprava/scalar-go"
)

// Handlers holds all HTTP handler dependencies
type Handlers struct {
	bapLookup     *bap.LookupService
	bsocialLookup *bsocial.LookupService
	beefStore     *beef.RedisBeefStorage
	engine        *engine.Engine
	cache         *redis.Client
	currentBlock  *func() uint32
	port          int
	subscribe     chan *subRequest
	unsubscribe   chan *subRequest
}

// NewHandlers creates a new Handlers instance with dependencies
func NewHandlers(
	bapLookup *bap.LookupService,
	bsocialLookup *bsocial.LookupService,
	beefStore *beef.RedisBeefStorage,
	eng *engine.Engine,
	cache *redis.Client,
	currentBlock *func() uint32,
	port int,
	subscribe chan *subRequest,
	unsubscribe chan *subRequest,
) *Handlers {
	return &Handlers{
		bapLookup:     bapLookup,
		bsocialLookup: bsocialLookup,
		beefStore:     beefStore,
		engine:        eng,
		cache:         cache,
		currentBlock:  currentBlock,
		port:          port,
		subscribe:     subscribe,
		unsubscribe:   unsubscribe,
	}
}

// @Summary Ingest raw transaction
// @Description Submit a raw BSV transaction for processing. Transaction is validated, input transactions loaded from BEEF store, converted to BEEF format, and submitted to overlay engine for BAP/BSocial indexing.
// @Tags Transaction
// @Accept application/octet-stream
// @Produce json
// @Param transaction body string true "Raw transaction bytes"
// @Success 200 {object} Response{result=object{txid=string}} "Transaction accepted with TXID"
// @Failure 400 {object} Response "Invalid transaction data"
// @Failure 404 {object} Response "Input transaction not found in BEEF store"
// @Failure 500 {object} Response "Failed to process transaction"
// @Router /ingest [post]
func (h *Handlers) IngestTransaction(c *fiber.Ctx) error {
	tx, err := transaction.NewTransactionFromBytes(c.Body())
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(Response{
			Status:  "ERROR",
			Message: "Invalid transaction data: " + err.Error(),
		})
	}

	for _, input := range tx.Inputs {
		sourceBeef, err := h.beefStore.LoadBeef(c.Context(), input.SourceTXID)
		if err != nil {
			return c.Status(fiber.StatusNotFound).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to load input transaction: " + err.Error(),
			})
		}
		input.SourceTransaction, err = transaction.NewTransactionFromBEEF(sourceBeef)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to parse input transaction: " + err.Error(),
			})
		}
	}

	taggedBeef := overlay.TaggedBEEF{
		Topics: []string{"tm_bap", "tm_bsocial"},
	}

	taggedBeef.Beef, err = tx.AtomicBEEF(false)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to generate BEEF: " + err.Error(),
		})
	}

	_, err = h.engine.Submit(c.Context(), taggedBeef, engine.SubmitModeHistorical, nil)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to submit transaction: " + err.Error(),
		})
	}

	return c.JSON(Response{
		Status: "OK",
		Result: map[string]any{
			"txid": tx.TxID().String(),
		},
	})
}

// @Summary Autofill search
// @Description Fast combined search across identities (top 3) and posts (top 10) for autocomplete/autofill functionality. Results cached for 15 minutes.
// @Tags Search
// @Accept json
// @Produce json
// @Param q query string true "Search query"
// @Success 200 {object} Response{result=object{identities=[]bap.Identity,posts=[]object}} "Combined search results"
// @Failure 500 {object} Response "Search failed"
// @Router /autofill [get]
func (h *Handlers) Autofill(c *fiber.Ctx) error {
	q := c.Query("q")

	if h.cache != nil {
		cached, err := h.cache.HGet(c.Context(), "autofill", q).Result()
		if err != nil {
			log.Println("Cache error:", err)
		} else if cached != "" {
			return c.JSON(Response{
				Status: "OK",
				Result: json.RawMessage(cached),
			})
		}
	}

	identities, err := h.bapLookup.Search(c.Context(), q, 3, 0)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to search identities: " + err.Error(),
		})
	}

	posts, err := h.bsocialLookup.Search(c.Context(), q, 10, 0)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to search posts: " + err.Error(),
		})
	}

	result, err := json.Marshal(map[string]any{
		"identities": identities,
		"posts":      posts,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to marshal result: " + err.Error(),
		})
	}

	if h.cache != nil {
		h.cache.Pipelined(c.Context(), func(pipe redis.Pipeliner) error {
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

// @Summary Search identities
// @Description Search for BAP identities using full-text search with MongoDB Atlas Search
// @Tags Identity
// @Accept json
// @Produce json
// @Param q query string true "Search query"
// @Param limit query int false "Results limit" default(20) minimum(1) maximum(100)
// @Param offset query int false "Results offset for pagination" default(0) minimum(0)
// @Success 200 {object} Response{result=[]bap.Identity} "List of matching identities"
// @Failure 500 {object} Response "Internal server error"
// @Router /identity/search [get]
func (h *Handlers) SearchIdentities(c *fiber.Ctx) error {
	q := c.Query("q")
	limit := c.QueryInt("limit", 20)
	offset := c.QueryInt("offset", 0)

	identities, err := h.bapLookup.Search(c.Context(), q, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to search identities: " + err.Error(),
		})
	}

	return c.JSON(Response{
		Status: "OK",
		Result: identities,
	})
}

// @Summary Search posts
// @Description Search for BSocial posts using full-text search with MongoDB Atlas Search
// @Tags Post
// @Accept json
// @Produce json
// @Param q query string true "Search query"
// @Param limit query int false "Results limit" default(20) minimum(1) maximum(100)
// @Param offset query int false "Results offset for pagination" default(0) minimum(0)
// @Success 200 {object} Response{result=[]object} "List of matching posts with BMAP data"
// @Failure 500 {object} Response "Internal server error"
// @Router /post/search [get]
func (h *Handlers) SearchPosts(c *fiber.Ctx) error {
	q := c.Query("q")
	limit := c.QueryInt("limit", 20)
	offset := c.QueryInt("offset", 0)

	posts, err := h.bsocialLookup.Search(c.Context(), q, limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to search posts: " + err.Error(),
		})
	}

	return c.JSON(Response{
		Status: "OK",
		Result: posts,
	})
}

// @Summary Validate identity by address
// @Description Check if an address was valid for a specific identity at a given block height or timestamp. Used for temporal identity verification with address rotation support.
// @Tags Identity
// @Accept json
// @Produce json
// @Param request body IdentityValidByAddressParams true "Validation parameters (if block and timestamp are 0, uses current block)"
// @Success 200 {object} Response{result=IdentityValidResponse} "Validation result with identity details"
// @Failure 404 {object} Response "Identity not found"
// @Failure 500 {object} Response "Internal server error"
// @Router /identity/validByAddress [post]
func (h *Handlers) ValidateIdentityByAddress(c *fiber.Ctx) error {
	req := &IdentityValidByAddressParams{}
	c.BodyParser(&req)

	if req.Block == 0 && req.Timestamp == 0 {
		req.Block = (*h.currentBlock)()
		// For timestamp, we'd need access to currentBlock.Header.Timestamp
		// but we only have the height function, so leave timestamp at 0
	}

	id, err := h.bapLookup.LoadIdentityByAddress(c.Context(), req.Address)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: err.Error(),
		})
	}

	if id == nil {
		return c.Status(fiber.StatusNotFound).JSON(Response{
			Status:  "ERROR",
			Message: fmt.Sprintf("Identity not found for address %s", req.Address),
		})
	}

	var valid bool
	var currentAddress string

	if req.Block > 0 {
		for _, addr := range id.Addresses {
			if addr.Block <= req.Block {
				currentAddress = addr.Address
			} else {
				break
			}
		}
		valid = currentAddress == req.Address
	} else {
		for _, addr := range id.Addresses {
			if addr.Timestamp <= req.Timestamp {
				currentAddress = addr.Address
			} else {
				break
			}
		}
		valid = currentAddress == req.Address
	}

	result := IdentityValidResponse{
		Identity: *id,
		ValidityRecord: ValidityRecord{
			Valid:     valid,
			Block:     req.Block,
			Timestamp: req.Timestamp,
		},
	}

	if valid {
		result.Profile = id.Profile
	}

	return c.JSON(Response{
		Status: "OK",
		Result: result,
	})
}

// @Summary Get profile image field
// @Description Retrieve and proxy an image from a profile field (e.g., avatar, header). Supports multiple formats: bitfs://, ORDFS URLs, data URLs, HTTP URLs, and raw TxIDs. Returns default avatar if field is empty.
// @Tags Profile
// @Accept json
// @Produce image/jpeg,image/png,image/gif,image/webp
// @Param field path string true "Profile field name (e.g., 'avatar', 'header', 'banner')"
// @Param bapId path string true "BAP Identity ID"
// @Success 200 {file} binary "Image data with appropriate Content-Type header"
// @Failure 400 {object} Response "Invalid field or BAPID"
// @Failure 404 {object} Response "Profile not found"
// @Failure 500 {object} Response "Internal server error or failed to fetch image"
// @Router /person/{field}/{bapId} [get]
func (h *Handlers) GetPersonImageField(c *fiber.Ctx) error {
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

	id, err := h.bapLookup.LoadIdentityById(c.Context(), bapId)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: err.Error(),
		})
	}

	if id == nil || id.Profile == nil {
		return c.Status(fiber.StatusNotFound).JSON(Response{
			Status:  "ERROR",
			Message: fmt.Sprintf("Profile not found for BAPID %s", bapId),
		})
	}

	imageUrl, imageExists := id.Profile[field].(string)
	if !imageExists || strings.TrimSpace(imageUrl) == "" {
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

		metaData := strings.TrimPrefix(imageUrl[:commaIndex], "data:")
		metaDataParts := strings.Split(metaData, ";")
		metaData = metaDataParts[0]
		base64Data := imageUrl[commaIndex+1:]

		mediaType, _, err := mime.ParseMediaType(metaData)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(Response{
				Status:  "ERROR",
				Message: "Invalid media type in data URL " + metaData + " " + err.Error(),
			})
		}

		imgData, err := base64.StdEncoding.DecodeString(base64Data)
		if err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(Response{
				Status:  "ERROR",
				Message: "Failed to decode base64 image data",
			})
		}

		c.Set("Content-Type", mediaType)
		return c.Send(imgData)
	}

	// Handle regular image URL
	if strings.HasPrefix(imageUrl, "bitfs://") {
		baseUrl := "https://ordfs.network/"
		path := strings.TrimPrefix(imageUrl, "bitfs://")
		parts := strings.Split(path, ".")
		if len(parts) >= 3 && parts[1] == "out" {
			txid := parts[0]
			imageUrl = baseUrl + txid
		} else {
			return c.Status(fiber.StatusBadRequest).JSON(Response{
				Status:  "ERROR",
				Message: "Invalid bitfs URL format",
			})
		}
	}

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

	imgData, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to read image data",
		})
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(imgData)
	}

	c.Set("Content-Type", contentType)
	return c.Send(imgData)
}

// @Summary List profiles
// @Description Get a paginated list of BAP profiles with their identity data
// @Tags Profile
// @Accept json
// @Produce json
// @Param limit query int false "Results limit" default(20) minimum(1) maximum(100)
// @Param offset query int false "Results offset for pagination" default(0) minimum(0)
// @Success 200 {object} Response{result=[]bap.Profile} "List of profiles"
// @Failure 500 {object} Response "Internal server error"
// @Router /profile [get]
func (h *Handlers) ListProfiles(c *fiber.Ctx) error {
	offset := c.QueryInt("offset", 0)
	limit := c.QueryInt("limit", 20)

	profiles, err := h.bapLookup.LoadProfiles(c.Context(), limit, offset)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to fetch profiles: " + err.Error(),
		})
	}

	return c.JSON(Response{
		Status: "OK",
		Result: profiles,
	})
}

// @Summary Get profile by BAP ID
// @Description Retrieve profile data for a specific BAP identity
// @Tags Profile
// @Accept json
// @Produce json
// @Param bapId path string true "BAP Identity ID"
// @Success 200 {object} Response{result=object} "Profile data as key-value map"
// @Failure 500 {object} Response "Internal server error"
// @Router /profile/{bapId} [get]
func (h *Handlers) GetProfileByBapId(c *fiber.Ctx) error {
	bapId := c.Params("bapId")

	identity, err := h.bapLookup.LoadIdentityById(c.Context(), bapId)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to fetch profile: " + err.Error(),
		})
	}

	return c.JSON(Response{
		Status: "OK",
		Result: identity.Profile,
	})
}

// @Summary Get identity by ID
// @Description Retrieve a complete BAP identity by its ID key (BapId)
// @Tags Identity
// @Accept json
// @Produce json
// @Param request body object{idKey=string} true "Identity request with idKey field"
// @Success 200 {object} Response{result=bap.Identity} "Identity details including addresses, profile, and attestations"
// @Failure 400 {object} Response "Invalid request body"
// @Failure 404 {object} Response "Identity not found"
// @Failure 500 {object} Response "Internal server error"
// @Router /identity/get [post]
func (h *Handlers) GetIdentity(c *fiber.Ctx) error {
	req := map[string]string{}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(Response{
			Status:  "ERROR",
			Message: "Invalid request body: " + err.Error(),
		})
	}

	id, err := h.bapLookup.LoadIdentityById(c.Context(), req["idKey"])
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to fetch identity: " + err.Error(),
		})
	}

	if id == nil {
		return c.Status(fiber.StatusNotFound).JSON(Response{
			Status:  "ERROR",
			Message: "Identity not found for ID key: " + req["idKey"],
		})
	}

	return c.JSON(Response{
		Status: "OK",
		Result: id,
	})
}

// @Summary Subscribe to real-time updates
// @Description Subscribe to real-time overlay updates via Server-Sent Events (SSE). Streams updates from Redis PubSub channels for specified topics. Connection is long-lived and automatically cleaned up on disconnect.
// @Tags Subscription
// @Produce text/event-stream
// @Param topics path string true "Comma-separated topic list (e.g., 'tm_bap,tm_bsocial')"
// @Success 200 {string} string "Event stream with SSE format: id (timestamp), event (channel), data (payload)"
// @Failure 400 {object} object{error=string} "Missing topics parameter"
// @Failure 404 {object} object{error=string} "No topics provided"
// @Header 200 {string} Content-Type "text/event-stream"
// @Header 200 {string} Cache-Control "no-cache"
// @Header 200 {string} Connection "keep-alive"
// @Router /subscribe/{topics} [get]
func (h *Handlers) SubscribeToTopics(c *fiber.Ctx) error {
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

	subReq := &subRequest{
		topics:  topics,
		msgChan: make(chan *redis.Message, 25),
	}
	h.subscribe <- subReq

	disconnected := make(chan struct{})

	ctx := c.Context()
	ctx.SetBodyStreamWriter(func(w *bufio.Writer) {
		fmt.Println("Client connected for topics:", topics)

		go func() {
			<-ctx.Done()
			fmt.Println("Context done, closing connection for topics:", topics)
			h.unsubscribe <- subReq
			close(disconnected)
		}()

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
					h.unsubscribe <- subReq
					return
				}
			}
		}
	})

	return nil
}

// @Summary Serve OpenAPI specification
// @Description Returns the OpenAPI 3.0 specification in JSON format
// @Tags Documentation
// @Produce json
// @Success 200 {object} object "OpenAPI 3.0 specification"
// @Router /openapi.json [get]
func (h *Handlers) ServeOpenAPISpec(c *fiber.Ctx) error {
	spec, err := swag.ReadDoc("swagger")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to read OpenAPI spec: " + err.Error(),
		})
	}
	c.Set("Content-Type", "application/json")
	return c.SendString(spec)
}

// @Summary API Documentation UI
// @Description Interactive API documentation powered by Scalar
// @Tags Documentation
// @Produce text/html
// @Success 200 {string} string "HTML documentation page"
// @Router /docs [get]
func (h *Handlers) ServeDocumentation(c *fiber.Ctx) error {
	// Build the full URL for the OpenAPI spec
	scheme := "http"
	if c.Protocol() == "https" {
		scheme = "https"
	}
	specURL := fmt.Sprintf("%s://%s/openapi.json", scheme, c.Hostname())

	html, err := scalar.NewV2(
		scalar.WithSpecURL(specURL),
		scalar.WithDarkMode(),
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(Response{
			Status:  "ERROR",
			Message: "Failed to render documentation: " + err.Error(),
		})
	}

	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(html)
}

// @Summary Landing page
// @Description Beautiful landing page with Sigma branding and link to API documentation
// @Tags Documentation
// @Produce text/html
// @Success 200 {string} string "HTML landing page"
// @Router / [get]
func (h *Handlers) ServeLandingPage(c *fiber.Ctx) error {
	html := `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>SIGMA - Decentralized Identity API</title>
    <style>
        * {
            margin: 0;
            padding: 0;
            box-sizing: border-box;
        }

        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', 'Roboto', 'Oxygen',
                'Ubuntu', 'Cantarell', 'Fira Sans', 'Droid Sans', 'Helvetica Neue', sans-serif;
            -webkit-font-smoothing: antialiased;
            -moz-osx-font-smoothing: grayscale;
            background: #000000;
            color: #ffffff;
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }

        .container {
            text-align: center;
            padding: 2rem;
            max-width: 800px;
        }

        .logo {
            width: 100%;
            max-width: 600px;
            height: auto;
            margin: 0 auto 3rem;
            filter: drop-shadow(0 4px 12px rgba(255, 255, 255, 0.1));
        }

        p {
            font-size: 1.25rem;
            color: #cbd5e0;
            margin-bottom: 3rem;
            line-height: 1.6;
        }

        .button {
            display: inline-block;
            background: #667eea;
            color: white;
            padding: 1rem 2.5rem;
            border-radius: 12px;
            text-decoration: none;
            font-weight: 600;
            font-size: 1.125rem;
            transition: all 0.3s ease;
            box-shadow: 0 4px 20px rgba(102, 126, 234, 0.4);
        }

        .button:hover {
            background: #5568d3;
            transform: translateY(-2px);
            box-shadow: 0 8px 30px rgba(102, 126, 234, 0.6);
        }

        .features {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 2rem;
            margin-top: 4rem;
        }

        .feature {
            background: rgba(255, 255, 255, 0.05);
            padding: 1.5rem;
            border-radius: 12px;
            border: 1px solid rgba(255, 255, 255, 0.1);
            backdrop-filter: blur(10px);
        }

        .feature h3 {
            font-size: 1.125rem;
            margin-bottom: 0.5rem;
            color: #a0aec0;
        }

        .feature p {
            font-size: 0.875rem;
            margin: 0;
            color: #718096;
        }

        @media (max-width: 640px) {
            p {
                font-size: 1rem;
            }

            .button {
                padding: 0.875rem 2rem;
                font-size: 1rem;
            }

            .features {
                grid-template-columns: 1fr;
            }
        }
    </style>
</head>
<body>
    <div class="container">
        <svg class="logo" viewBox="0 0 1135.74 367.32" xmlns="http://www.w3.org/2000/svg">
            <defs><style>.cls-1{fill:#ffffff;}</style></defs>
            <g><path class="cls-1" d="M59.17,183.71c0,14.48,11.59,26.07,25.65,26.07,15.31,0,24.83-8.69,24.83-21.52,0-18.62-24.41-24-44.69-31.03C23.17,142.33,0,122.89,0,80.27S37.65,0,84.41,0c55.03,0,81.1,35.17,84.41,81.1h-57.1c0-14.07-8.69-25.65-25.65-25.65-13.24,0-26.07,7.86-26.07,24,0,18.62,22.76,21.93,43.86,28.14,44.27,13.24,64.96,38.07,64.96,76.96,0,43.86-37.24,80.27-83.99,80.27C30.62,264.8,0,228.39,0,183.71H59.17Z"/><path class="cls-1" d="M209.77,8.28h60.82v248.25h-60.82V8.28Z"/><path class="cls-1" d="M443.54,0c45.93,0,77.79,16.96,103.85,43.44l-42.2,38.89c-16.96-15.31-38.07-24.83-61.65-24.83-44.27,0-72.82,33.1-72.82,74.89,0,45.93,32.69,74.89,74.06,74.89,43.45,0,64.55-24,66.2-48h-70.75v-52.55h136.54v23.58c0,73.24-43.03,134.47-133.23,134.47-78.61,0-134.47-58.34-134.47-132.4S364.93,0,443.54,0Z"/><path class="cls-1" d="M616.49,8.28h56.68l63.3,134.06L800.19,8.28h55.86v248.25h-60.41v-110.47l-54.2,118.75h-9.93l-54.2-119.58v111.3h-60.82V8.28Z"/><path class="cls-1" d="M1003.34,0h10.76l121.64,256.53h-65.37l-12.41-25.65h-98.06l-12,25.65h-65.37L1003.34,0Zm33.93,187.43l-28.55-62.89h-.83l-28.96,62.89h58.34Z"/></g>
            <g><path class="cls-1" d="M11.83,317.37H28.67c14.26,0,25.3,10.47,25.3,24.09,0,12.89-9.83,24.25-25.3,24.25H11.83v-48.34Zm16.6,37.54c7.57,0,13.37-5.32,13.37-13.45,0-7.49-5.08-13.38-13.37-13.38h-4.75v26.83h4.75Z"/><path class="cls-1" d="M62.02,317.37h31.99v10.47h-20.14v8.7h18.61v10.31h-18.61v8.7h21.03v10.15H62.02v-48.34Z"/><path class="cls-1" d="M127.28,315.75c9.18,0,15.55,3.46,20.71,8.86l-8.14,7.73c-3.38-3.3-7.73-5.4-12.57-5.4-8.62,0-14.18,6.45-14.18,14.58s5.56,14.58,14.18,14.58c4.83,0,9.18-2.09,12.57-5.4l7.89,7.73c-4.83,5.08-11.52,8.86-20.46,8.86-15.31,0-26.18-11.36-26.18-25.78s10.88-25.78,26.18-25.78Z"/><path class="cls-1" d="M153.71,317.37h31.99v10.47h-20.14v8.7h18.61v10.31h-18.61v8.7h21.03v10.15h-32.87v-48.34Z"/><path class="cls-1" d="M192.3,317.37h12.49l18.77,28.68v-28.68h11.76v48.34h-12.33l-18.85-28.76v28.76h-11.84v-48.34Z"/><path class="cls-1" d="M251.6,327.84h-10.8v-10.47h33.68v10.47h-10.96v37.87h-11.92v-37.87Z"/><path class="cls-1" d="M279.96,317.37h20.22c10.39,0,17.16,6.28,17.16,15.39,0,8.46-4.27,13.21-11.44,14.98l13.05,17.97h-14.82l-12.33-18.05v18.05h-11.84v-48.34Zm21.35,20.95c2.66,0,4.43-2.74,4.43-5.56s-2.09-5.32-4.75-5.32h-9.18v10.88h9.51Z"/><path class="cls-1" d="M344.09,315.75h2.09l23.69,49.95h-12.73l-2.42-5h-19.09l-2.34,5h-12.73l23.53-49.95Zm6.61,36.5l-5.56-12.25h-.16l-5.64,12.25h11.36Z"/><path class="cls-1" d="M375.03,317.37h11.84v38.19h21.03v10.15h-32.87v-48.34Z"/><path class="cls-1" d="M413.62,317.37h11.84v48.34h-11.84v-48.34Z"/><path class="cls-1" d="M431.02,364.58l22.07-36.33h-19.34v-10.88h37.95v1.13l-22.88,36.25h22.08v10.96h-39.88v-1.13Z"/><path class="cls-1" d="M478.96,317.37h31.98v10.47h-20.14v8.7h18.61v10.31h-18.61v8.7h21.03v10.15h-32.87v-48.34Z"/><path class="cls-1" d="M517.55,317.37h16.84c14.26,0,25.3,10.47,25.3,24.09,0,12.89-9.83,24.25-25.3,24.25h-16.84v-48.34Zm16.6,37.54c7.57,0,13.37-5.32,13.37-13.45,0-7.49-5.08-13.38-13.37-13.38h-4.75v26.83h4.75Z"/><path class="cls-1" d="M586.81,320.3h15.82c13.4,0,23.77,9.84,23.77,22.63,0,12.11-9.23,22.78-23.77,22.78h-15.82v-45.41Zm15.59,35.27c7.11,0,12.56-5,12.56-12.64,0-7.04-4.77-12.56-12.56-12.56h-4.46v25.2h4.46Z"/><path class="cls-1" d="M633.96,320.3h11.13v45.41h-11.13v-45.41Z"/><path class="cls-1" d="M676.72,318.78c8.4,0,14.23,3.1,19,7.95l-7.72,7.11c-3.1-2.8-6.96-4.54-11.28-4.54-8.1,0-13.32,6.05-13.32,13.7,0,8.4,5.98,13.7,13.55,13.7,7.95,0,11.81-4.39,12.11-8.78h-12.94v-9.61h24.98v4.31c0,13.4-7.87,24.6-24.37,24.6-14.38,0-24.6-10.67-24.6-24.22s10.22-24.22,24.6-24.22Z"/><path class="cls-1" d="M708.35,320.3h11.13v45.41h-11.13v-45.41Z"/><path class="cls-1" d="M734.77,330.13h-10.14v-9.84h31.64v9.84h-10.29v35.57h-11.2v-35.57Z"/><path class="cls-1" d="M774.27,318.78h1.97l22.25,46.92h-11.96l-2.27-4.69h-17.94l-2.2,4.69h-11.96l22.1-46.92Zm6.21,34.29l-5.22-11.5h-.15l-5.3,11.5h10.67Z"/><path class="cls-1" d="M803.34,320.3h11.13v35.87h19.75v9.54h-30.88v-45.41Z"/><path class="cls-1" d="M857.38,320.3h11.12v45.41h-11.12v-45.41Z"/><path class="cls-1" d="M875.77,320.3h15.82c13.4,0,23.76,9.84,23.76,22.63,0,12.11-9.23,22.78-23.76,22.78h-15.82v-45.41Zm15.59,35.27c7.11,0,12.56-5,12.56-12.64,0-7.04-4.77-12.56-12.56-12.56h-4.47v25.2h4.47Z"/><path class="cls-1" d="M922.92,320.3h30.05v9.84h-18.92v8.17h17.48v9.69h-17.48v8.17h19.75v9.54h-30.88v-45.41Z"/><path class="cls-1" d="M959.17,320.3h11.73l17.63,26.94v-26.94h11.05v45.41h-11.58l-17.71-27.02v27.02h-11.12v-45.41Z"/><path class="cls-1" d="M1014.87,330.13h-10.14v-9.84h31.64v9.84h-10.29v35.57h-11.2v-35.57Z"/><path class="cls-1" d="M1041.51,320.3h11.13v45.41h-11.13v-45.41Z"/><path class="cls-1" d="M1067.92,330.13h-10.14v-9.84h31.64v9.84h-10.29v35.57h-11.2v-35.57Z"/><path class="cls-1" d="M1108.34,343.76l-16.12-23.46h12.56l9.08,13.62,9.16-13.62h12.49l-16.12,23.46v21.95h-11.05v-21.95Z"/></g>
        </svg>

        <p>Bitcoin SV overlay service for BAP identities and BSocial interactions. Providing real-time subscription capabilities and comprehensive REST API endpoints.</p>

        <a href="/docs" class="button">View API Documentation →</a>

        <div class="features">
            <div class="feature">
                <h3>BAP Protocol</h3>
                <p>Bitcoin Attestation Protocol for identity management</p>
            </div>
            <div class="feature">
                <h3>Real-time</h3>
                <p>SSE subscriptions for live updates</p>
            </div>
            <div class="feature">
                <h3>Scalable</h3>
                <p>Redis & MongoDB powered infrastructure</p>
            </div>
        </div>
    </div>
</body>
</html>`

	c.Set("Content-Type", "text/html; charset=utf-8")
	return c.SendString(html)
}
