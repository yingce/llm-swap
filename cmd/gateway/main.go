package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"llm-swap/internal/config"
	"llm-swap/internal/gateway"
)

func main() {
	runtime, err := config.LoadGatewayRuntime(context.Background(), config.GatewayRuntimeOptions{
		Args: os.Args[1:],
	})
	if err != nil {
		log.Fatal(err)
	}

	srv := gateway.NewServerWithGatewayPersistence(runtime.Config, gateway.DefaultGatewayRequestLogPath)
	go srv.RunLoadedReconciler(context.Background(), 30*time.Second)

	log.Fatal(http.ListenAndServe(runtime.ListenAddr, srv))
}
