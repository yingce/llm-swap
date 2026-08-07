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
	ctx := context.Background()
	runtime, err := config.LoadGatewayRuntime(ctx, config.GatewayRuntimeOptions{
		Args: os.Args[1:],
	})
	if err != nil {
		log.Fatal(err)
	}

	srv, err := gateway.NewServerWithConfigRevisionStore(
		ctx,
		runtime.Config,
		gateway.DefaultGatewayRequestLogPath,
		gateway.DefaultGatewayWorkerEventLogPath,
		runtime.ConfigPath,
		runtime.Overrides,
		gateway.NewFileConfigRevisionStore(gateway.DefaultGatewayConfigRevisionPath),
	)
	if err != nil {
		log.Fatal(err)
	}
	go srv.RunLoadedReconciler(ctx, 30*time.Second)

	log.Fatal(http.ListenAndServe(runtime.ListenAddr, srv))
}
