// Run with go run ./scripts/backfill-conversation-themes.go -db /path/to/shelley.db -input chapters.json.
// Input is an object keyed by conversation ID, containing chapters and through_message_id.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"shelley.exe.dev/db"
)

func main() {
	path := flag.String("db", "", "Shelley database path")
	input := flag.String("input", "", "JSON file keyed by conversation ID")
	flag.Parse()
	if *path == "" || *input == "" {
		log.Fatal("-db and -input are required")
	}
	raw, err := os.ReadFile(*input)
	if err != nil {
		log.Fatal(err)
	}
	var themes map[string]db.ConversationThemes
	if err := json.Unmarshal(raw, &themes); err != nil {
		log.Fatal(err)
	}
	database, err := db.New(db.Config{DSN: *path})
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	if err := database.Migrate(ctx); err != nil {
		log.Fatal(err)
	}
	for id, chapters := range themes {
		if err := database.SetConversationThemes(ctx, id, chapters); err != nil {
			log.Fatalf("%s: %v", id, err)
		}
		fmt.Printf("%s: %d chapters\n", id, len(chapters.Chapters))
	}
}
