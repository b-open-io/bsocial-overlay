# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**bsocial-overlay** is a Go-based Bitcoin SV overlay service that indexes and serves BAP (Bitcoin Attestation Protocol) identities and BSocial posts/interactions from the blockchain. It implements the BSV Overlay Services architecture, providing real-time subscription capabilities and REST API endpoints for identity verification and social data lookup.

## Architecture

### Core Components

1. **BAP (Bitcoin Attestation Protocol) Overlay** (`bap/`)
   - Identity management and verification
   - Attestation tracking and validation
   - Profile data storage
   - Address rotation history

2. **BSocial Overlay** (`bsocial/`)
   - Social posts (posts, likes, reposts, messages)
   - Follow/unfollow relationships
   - BMAP transaction parsing

3. **Overlay Engine Integration**
   - Uses `github.com/bsv-blockchain/go-overlay-services` for core overlay logic
   - Topic managers (`TopicManager`) determine output admissibility
   - Lookup services (`LookupService`) handle data retrieval and indexing
   - Storage layer abstracts MongoDB and Redis persistence

4. **Server** (`cmd/server/`)
   - Fiber v2 HTTP server
   - REST API endpoints
   - Server-Sent Events (SSE) for real-time subscriptions
   - Redis PubSub broadcasting
   - Image proxy (ORDFS, bitfs:// URLs, base64 data URLs)

5. **Processors** (`cmd/processBAP/`, `cmd/processBSocial/`)
   - Junglebus blockchain subscription
   - Historical transaction processing from Redis queues
   - BEEF (Bitcoin Extended Encoding Format) transaction parsing

### Data Flow

1. **Ingestion**: Transactions arrive via Junglebus subscription or `/api/v1/ingest` endpoint
2. **Topic Manager**: Determines which outputs are admissible for BAP/BSocial topics
3. **Lookup Service**: Processes admitted outputs and updates MongoDB collections
4. **Publishing**: Changes broadcast to Redis PubSub channels
5. **SSE Streaming**: Connected clients receive real-time updates via `/api/v1/subscribe/:topics`

### Storage Architecture

- **MongoDB**: Primary data storage
  - `identities` collection: BAP identities with addresses, profiles
  - `attestations` collection: Attestations with signers
  - `post`, `like`, `follow`, `unfollow` collections: BSocial data
  - Atlas Search indexes for full-text search

- **Redis**: Caching and real-time communication
  - Transaction queues for processing
  - BEEF storage (5-day TTL)
  - PubSub channels for real-time updates
  - Autofill cache (15-minute TTL)

## Development Commands

### Building

```bash
# Build the server (generates Swagger docs and builds binary)
./build.sh

# Manual build (without Swagger generation)
go build -o server.run ./cmd/server

# Generate Swagger documentation only
swag init -g cmd/server/server.go -o ./docs --parseDependency --parseInternal

# Build specific processor
go build -o processBAP.run cmd/processBAP/processBAP.go
go build -o processBSocial.run cmd/processBSocial/processBSocial.go
```

### Running

```bash
# Start server (default port 3000)
./server.run

# Start server with custom port and sync
./server.run -p 8080 -s

# Start BAP processor
./processBAP.run -t <junglebus-topic-id> -s 575000

# Start BSocial processor
./processBSocial.run -t <junglebus-topic-id> -s 575000
```

### Testing

```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run tests in specific package
go test ./bap
go test ./bsocial
```

### Development

```bash
# Install dependencies
go mod tidy

# Update dependencies
go get -u ./...

# Format code
go fmt ./...

# Lint (if golangci-lint is installed)
golangci-lint run
```

## Environment Variables

Required environment variables (create `.env` file):

```bash
# Blockchain infrastructure
BLOCK_HEADERS_URL=https://headers.taal.com
BLOCK_HEADERS_API_KEY=your_api_key

# Database connections
MONGO_URL=mongodb://localhost:27017
REDIS=redis://localhost:6379
REDIS_BEEF=redis://localhost:6379/1

# Junglebus subscription
JUNGLEBUS=https://junglebus.gorillapool.io
TOPIC=your_subscription_id

# Server configuration
PORT=3000
HOSTING_URL=https://your-domain.com

# Peer synchronization (comma-separated URLs)
PEERS=https://peer1.com,https://peer2.com

# ARC broadcaster
ARC_API_KEY=your_arc_key
ARC_CALLBACK_TOKEN=your_callback_token
```

## API Endpoints

### Identity Endpoints

- `POST /api/v1/identity/get` - Get identity by ID key
- `GET /api/v1/identity/search?q=query&limit=20&offset=0` - Search identities
- `POST /api/v1/identity/validByAddress` - Validate identity address at block/timestamp

### Profile Endpoints

- `GET /api/v1/profile?limit=20&offset=0` - List profiles
- `GET /api/v1/profile/:bapId` - Get profile by BAP ID
- `GET /api/v1/person/:field/:bapId` - Get profile field as image (avatar, header, etc.)

### Post Endpoints

- `GET /api/v1/post/search?q=query&limit=20&offset=0` - Search posts

### Utility Endpoints

- `GET /api/v1/autofill?q=query` - Search identities and posts (cached)
- `POST /api/v1/ingest` - Ingest raw transaction
- `GET /api/v1/subscribe/:topics` - Subscribe to real-time updates (SSE)

### Documentation Endpoints

- `GET /docs` - Interactive API documentation (Scalar UI)
- `GET /openapi.json` - OpenAPI 3.0 specification

### Overlay Service Endpoints

Standard overlay service endpoints are registered via `server.RegisterRoutesWithErrorHandler`:
- Transaction submission
- UTXO lookups
- Topic synchronization
- See `github.com/bsv-blockchain/go-overlay-services` documentation

## Key Concepts

### BAP Operations

1. **ID**: Creates new identity or rotates address
   - First occurrence creates identity with `BapId`, `RootAddress`
   - Subsequent occurrences rotate to new `CurrentAddress`

2. **ATTEST**: Creates attestation signed by identity
   - Must be signed by current address of existing identity
   - Creates `Signer` record linked to identity

3. **REVOKE**: Revokes attestation
   - Marks attestation as revoked

4. **ALIAS**: Updates profile data
   - Must be signed by current address
   - Stores arbitrary JSON profile data

### BSocial Operations

- **post**: Create content post
- **like**: Like a transaction
- **repost**: Repost content
- **message**: Direct message
- **follow/unfollow**: Relationship tracking

All BSocial operations use BMAP (Bitcoin Metadata Attribute Protocol) with MAP, AIP, and B protocols.

### BEEF (Bitcoin Extended Encoding Format)

Transactions are stored and transmitted in BEEF format, which includes:
- Transaction data
- Merkle proofs for SPV
- Parent transactions for input validation

### Image URL Formats

The `/api/v1/person/:field/:bapId` endpoint handles:
- `bitfs://` URLs → converts to ORDFS network URLs
- Raw TxIDs → prefixes with `/` and fetches from ORDFS
- `data:` URLs → decodes base64 inline images
- HTTP(S) URLs → proxies external images
- Default: Returns default avatar if field is empty

## MongoDB Atlas Search

Both BAP and BSocial lookup services use MongoDB Atlas Search for full-text queries:
- Index name: `default`
- Wildcard path search across all fields
- Pagination via `$skip` and `$limit` in aggregation pipeline

## Real-time Subscriptions

Server implements custom SSE (Server-Sent Events) for real-time updates:
1. Client connects to `/api/v1/subscribe/tm_bap,tm_bsocial`
2. Server subscribes to Redis PubSub channels matching topics
3. Messages broadcast to all connected clients on matching topics
4. Automatic cleanup on client disconnect

## Development Notes

- All transactions must include valid AIP signatures
- Identity lookups use address history for temporal validation
- Block height determines address validity for identity verification
- BEEF storage has 5-day TTL - older transactions require re-fetch
- The server gracefully handles shutdown via OS signals (SIGINT, SIGTERM)
- Redis connection pooling configured with max 100 connections for BSocial
- UTF-8 validation performed before MongoDB insertion to prevent errors

## Common Tasks

### Adding New BAP Operation Type

1. Add case in `bap/lookup.go` `OutputAdmittedByTopic` switch
2. Implement data persistence logic
3. Update `bap/topic.go` `IdentifyAdmissibleOutputs` to admit outputs

### Adding New BSocial Type

1. Update `bsocial/types.go` `OutputTypes` constant
2. Add MongoDB collection and indexes in `bsocial/lookup.go` `NewLookupService`
3. Ensure BMAP structure matches in `PrepareForIngestion`

### Adding New API Endpoint

1. Add route in `cmd/server/server.go` after overlay service registration
2. Use `Response` struct for consistent JSON formatting
3. Handle errors with appropriate HTTP status codes
4. Cache results in Redis if appropriate

## Dependencies

Key dependencies:
- `github.com/bsv-blockchain/go-sdk` - BSV transaction handling
- `github.com/bsv-blockchain/go-overlay-services` - Overlay engine core
- `github.com/bitcoinschema/go-bmap` - BMAP protocol parsing
- `github.com/gofiber/fiber/v2` - HTTP server
- `go.mongodb.org/mongo-driver` - MongoDB client
- `github.com/redis/go-redis/v9` - Redis client
- `github.com/GorillaPool/go-junglebus` - Blockchain subscription

## Replace Directives

The project uses custom forks for specific functionality:
- `go-overlay-services`: Custom version for overlay features
- `go-templates`: Custom bitcom template decoding
- `go-bpu`: Custom BPU parsing
