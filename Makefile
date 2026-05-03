.PHONY: lint test cover

lint:
	golangci-lint run ./...

test:
	go test ./... -race -cover

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out
