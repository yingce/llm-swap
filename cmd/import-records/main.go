package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"llm-swap/internal/gateway"
)

type importSink interface {
	AppendImportedRequestRecord(context.Context, gateway.RequestLogEntry, string) (bool, error)
	AppendImportedWorkerEvent(context.Context, gateway.WorkerEventRecord, string) (bool, error)
}

type importStats struct {
	Total      int
	Inserted   int
	Duplicates int
	Invalid    int
}

func main() {
	var dsn string
	var requestPath string
	var eventPath string
	var gatewayID string
	var autoMigrate bool
	var timeoutMS int
	flag.StringVar(&dsn, "dsn", firstNonEmpty(os.Getenv("PG_DSN"), os.Getenv("LLMSWAP_RECORDS_STORE_DSN")), "Postgres DSN")
	flag.StringVar(&requestPath, "requests", gateway.DefaultGatewayRequestLogPath, "gateway request JSONL path")
	flag.StringVar(&eventPath, "events", gateway.DefaultGatewayWorkerEventLogPath, "gateway worker event JSONL path")
	flag.StringVar(&gatewayID, "gateway-id", os.Getenv("LLMSWAP_RECORDS_STORE_GATEWAY_ID"), "gateway id for imported records")
	flag.BoolVar(&autoMigrate, "auto-migrate", true, "run embedded records store migration before import")
	flag.IntVar(&timeoutMS, "timeout-ms", 3000, "Postgres operation timeout in milliseconds")
	flag.Parse()

	if strings.TrimSpace(dsn) == "" {
		log.Fatal("PG_DSN or --dsn is required")
	}

	ctx := context.Background()
	store, err := gateway.NewPostgresRecordsStore(ctx, dsn, gatewayID, time.Duration(timeoutMS)*time.Millisecond, autoMigrate)
	if err != nil {
		log.Fatalf("connect records store: %v", err)
	}
	defer store.Close()

	requestStats, err := importRequestLog(ctx, requestPath, store)
	if err != nil {
		log.Fatalf("import requests: %v", err)
	}
	eventStats, err := importWorkerEventLog(ctx, eventPath, store)
	if err != nil {
		log.Fatalf("import worker events: %v", err)
	}

	fmt.Printf("requests total=%d inserted=%d duplicates=%d invalid=%d\n", requestStats.Total, requestStats.Inserted, requestStats.Duplicates, requestStats.Invalid)
	fmt.Printf("worker_events total=%d inserted=%d duplicates=%d invalid=%d\n", eventStats.Total, eventStats.Inserted, eventStats.Duplicates, eventStats.Invalid)
}

func importRequestLog(ctx context.Context, path string, sink importSink) (importStats, error) {
	return importJSONL(ctx, path, func(line []byte) (bool, bool, error) {
		var entry gateway.RequestLogEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			return false, false, nil
		}
		if entry.RequestID == "" || entry.Model == "" || entry.Time.IsZero() {
			return false, false, nil
		}
		inserted, err := sink.AppendImportedRequestRecord(ctx, entry, lineHash("request", line))
		return inserted, true, err
	})
}

func importWorkerEventLog(ctx context.Context, path string, sink importSink) (importStats, error) {
	return importJSONL(ctx, path, func(line []byte) (bool, bool, error) {
		var event gateway.WorkerEventRecord
		if err := json.Unmarshal(line, &event); err != nil {
			return false, false, nil
		}
		if event.WorkerID == "" || event.Event == "" || event.ReceivedAt.IsZero() {
			return false, false, nil
		}
		inserted, err := sink.AppendImportedWorkerEvent(ctx, event, lineHash("worker_event", line))
		return inserted, true, err
	})
}

func importJSONL(ctx context.Context, path string, importLine func([]byte) (bool, bool, error)) (importStats, error) {
	var stats importStats
	if strings.TrimSpace(path) == "" {
		return stats, nil
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return stats, nil
		}
		return stats, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		copied := append([]byte(nil), line...)
		stats.Total++
		inserted, valid, err := importLine(copied)
		if err != nil {
			return stats, err
		}
		if inserted {
			stats.Inserted++
			continue
		}
		if !valid {
			stats.Invalid++
		} else {
			stats.Duplicates++
		}
	}
	if err := scanner.Err(); err != nil {
		return stats, err
	}
	return stats, nil
}

func lineHash(kind string, line []byte) string {
	sum := sha256.Sum256(append([]byte(kind+"\x00"), line...))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
