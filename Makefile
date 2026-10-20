# Common developer tasks. On Windows without make, run the commands directly.

.PHONY: proto build test race run robot up down lint

proto:            ## regenerate Go code from proto/ (needs buf + protoc-gen-go)
	buf generate

build:            ## build server and robot binaries into bin/
	go build -o bin/ ./cmd/...

test:             ## run every test
	go test ./...

race:             ## run tests with the race detector (needs cgo)
	go test -race ./...

run:              ## run an all-in-one server with zero dependencies
	go run ./cmd/server -config configs/allinone.yaml

robot:            ## run 10 robots against a local server
	go run ./cmd/robot -addr 127.0.0.1:7000 -n 10 -duration 15s

up:               ## start the distributed cluster in Docker
	docker compose -f deploy/docker-compose.yml up -d --build

down:             ## stop the distributed cluster
	docker compose -f deploy/docker-compose.yml down

lint:
	gofmt -l . && go vet ./...
