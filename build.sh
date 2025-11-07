#!/bin/bash

echo "Generating API documentation..."
swag init -g cmd/server/server.go -o ./docs --parseDependency --parseInternal

echo "Building server..."
go build -o server.run ./cmd/server

echo "Building subscriber/crawler..."
go build -o crawl.run ./cmd/sub

echo "Build complete! Generated files:"
echo "  - server.run (API server)"
echo "  - crawl.run (subscriber/crawler)"
echo "  - docs/swagger.json (OpenAPI spec)"
echo "  - docs/swagger.yaml (OpenAPI spec)"
echo "  - docs/docs.go (embedded docs)"