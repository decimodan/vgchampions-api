BINARY_NAME := redis-pg-migrate
CMD := ./cmd/redis-pg-migrate
LDFLAGS ?= -s -w

.PHONY: build build-linux-amd64 build-linux-arm64 clean

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY_NAME) $(CMD)

build-linux-amd64:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY_NAME)-linux-amd64 $(CMD)

build-linux-arm64:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BINARY_NAME)-linux-arm64 $(CMD)

clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-linux-amd64 $(BINARY_NAME)-linux-arm64
