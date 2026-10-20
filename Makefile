# Common developer tasks. On Windows without make, run the commands directly.

.PHONY: proto build test race run robot up down lint operator-test crds kind-up k8s-install k8s-e2e

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

operator-test:    ## operator unit and reconciler tests
	cd operator && go test ./...

crds:             ## regenerate operator deepcopy, CRD and RBAC; copy the CRD into the chart
	cd operator && go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.23.0 object crd rbac:roleName=gameserver-operator 		paths=./api/v1alpha1 paths=./internal/controller 		output:crd:artifacts:config=config/crd output:rbac:artifacts:config=config/rbac
	cp operator/config/crd/*.yaml deploy/helm/gameserver/crds/

kind-up:          ## local Kubernetes cluster with the server and operator images loaded
	kind create cluster --name gs
	docker build -t gameserver:dev -f deploy/Dockerfile .
	docker build -t gameserver-operator:dev operator/
	echo 'FROM gameserver:dev' | docker build --label v=2 -t gameserver:e2e-v2 -
	kind load docker-image gameserver:dev gameserver:e2e-v2 gameserver-operator:dev --name gs

k8s-install:      ## install the chart (operator, dev infra, sample GameCluster)
	helm install gs deploy/helm/gameserver -n game --create-namespace

k8s-e2e:          ## end-to-end operator test on the current cluster (~8 min)
	bash test/k8s/e2e.sh

lint:
	gofmt -l . && go vet ./...
