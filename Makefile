COMPOSE ?= docker compose

.PHONY: shell test tidy

shell:
	$(COMPOSE) run --rm dev

test:
	$(COMPOSE) run --rm dev go test ./...

tidy:
	$(COMPOSE) run --rm dev go mod tidy
