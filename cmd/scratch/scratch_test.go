package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"testing"

	"github.com/bsv-blockchain/go-sdk/spv"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/chaintracker/headers_client"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
)

func init() {
	// Load environment variables from .env file
	if err := godotenv.Load("../../.env"); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	// Initialize the chaintracker client
	// chaintracker = headers_client.Client{
	// 	Url:    os.Getenv("BLOCK_HEADERS_URL"),
	// 	ApiKey: os.Getenv("BLOCK_HEADERS_API_KEY"),
	// }

	// // Parse command line flags
	// flag.StringVar(&TOPIC, "t", os.Getenv("TOPIC"), "Junglebus SubscriptionID")
	// flag.UintVar(&FROM_BLOCK, "s", 575000, "Start from block")
	// flag.Parse()

	// Create a new Junglebus client
	// var err error
	// if jb, err = junglebus.New(
	// 	junglebus.WithHTTP(os.Getenv("JUNGLEBUS")),
	// ); err != nil {
	// 	log.Fatalf("Failed to create Junglebus client: %v", err)
	// }
}

func main() {

}

func TestEnvironmentVariables(t *testing.T) {
	resp, err := http.Get("https://ordinals.1sat.app/v5/tx/3d244ffcc4ffca8b0260ed9481e33d024f7b018f925d4f661ac7fe6e0e68c1cd/beef")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "Expected status code 200 OK")

	chaintracker := headers_client.Client{
		Url:    os.Getenv("BLOCK_HEADERS_URL"),
		ApiKey: os.Getenv("BLOCK_HEADERS_API_KEY"),
	}

	beefBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "Failed to read response body")
	_, tx, _, err := transaction.ParseBeef(beefBytes)
	require.NoError(t, err, "Failed to parse beef")
	if tx.MerklePath == nil {
		log.Println("No MerklePath found in transaction, skipping height check")
		return
	}
	valid, err := spv.Verify(tx, chaintracker, nil)
	require.NoError(t, err, "Failed to verify transaction")
	require.True(t, valid, "Transaction should be valid")
	log.Println("Transaction is valid, MerklePath BlockHeight:", tx.MerklePath.BlockHeight)
}
