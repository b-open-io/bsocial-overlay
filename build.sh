#!/bin/bash

echo "Generating API documentation..."
swag init -g cmd/server/server.go -o ./docs --parseDependency --parseInternal

echo "Building server..."
go build -o server.run ./cmd/server

echo "Build complete! Generated files:"
echo "  - server.run (executable)"
echo "  - docs/swagger.json (OpenAPI spec)"
echo "  - docs/swagger.yaml (OpenAPI spec)"
echo "  - docs/docs.go (embedded docs)"