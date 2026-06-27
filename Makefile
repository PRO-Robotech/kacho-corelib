# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

.PHONY: lint test cover install-proto-plugins proto-gen

lint:
	golangci-lint run ./...

test:
	go test ./... -race -cover

cover:
	go test ./... -coverprofile=coverage.out
	go tool cover -func=coverage.out

# install-proto-plugins — ставит buf-плагины для генерации Go-stubs инфра-proto.
# Версии запиннены под go.mod (protobuf v1.36.x, grpc-gateway v2.29.x).
install-proto-plugins:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
	go install github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-grpc-gateway@v2.29.0

# proto-gen — линт + регенерация Go-stubs в proto/gen/go. Перед запуском —
# `make install-proto-plugins` (плагины должны быть на $PATH).
proto-gen:
	cd proto && buf lint && buf generate
