package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"llm-swap/internal/config"
	"llm-swap/internal/gateway"
)

func main() {
	configPath := flag.String("config", "examples/gateway.yaml", "gateway config path")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	f, err := os.Open(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	cfg, err := config.LoadGateway(f)
	if err != nil {
		log.Fatal(err)
	}

	log.Fatal(http.ListenAndServe(*addr, gateway.NewServer(cfg)))
}
