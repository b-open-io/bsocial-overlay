package main

import (
	"log"

	"github.com/joho/godotenv"
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
