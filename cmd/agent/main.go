package main

import (
	"flag"
	"log"
	"os"

	"llm-swap/internal/config"
)

func main() {
	configPath := flag.String("config", "examples/agent.yaml", "agent config path")
	flag.Parse()

	f, err := os.Open(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if _, err := config.LoadAgent(f); err != nil {
		log.Fatal(err)
	}
	log.Println("agent config loaded")
}
