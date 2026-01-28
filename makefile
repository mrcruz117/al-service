
SHELL_PATH = /bin/ash
SHELL = $(if $(wildcard $(SHELL_PATH)),/bin/ash,/bin/bash)

# RSA Keys
# 	To generate a private/public key PEM file.
# 	$ openssl genpkey -algorithm RSA -out private.pem -pkeyopt rsa_keygen_bits:2048
# 	$ openssl rsa -pubout -in private.pem -out public.pem

run:
	go run apis/services/sales/main.go | go run apis/tooling/logfmt/main.go

help:
	go run apis/services/sales/main.go --help

version:
	go run apis/services/sales/main.go --version

curl-live:
	curl -il -X GET http://localhost:3000/liveness

# curl-ready:
	curl -il -X GET http://localhost:3000/readiness

curl-error:
	curl -il -X GET http://localhost:3000/testerror

curl-panic:
	curl -il -X GET http://localhost:3000/testpanic

admin:
	go run apis/tooling/admin/main.go


# admin token
# export TOKEN=eyJhbGciOiJSUzI1NiIsImtpZCI6IjU0YmIyMTY1LTcxZTEtNDFhNi1hZjNlLTdkYTRhMGUxZTJjMSIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzZXJ2aWNlIHByb2plY3QiLCJzdWIiOiIzOGRjOWQ4NC0wMThiLTRhMTUtYjk1OC0wYjc4YWYxMWMzMDEiLCJleHAiOjE3ODQ4MzY3ODAsImlhdCI6MTc1MzMwMDc4MCwiUm9sZXMiOlsiQURNSU4iXX0.JFdB3frhUcDxFtOnTZcA2hAO7vc46PgGM8_Ldcs0-9zvHIMRAntFCoJhUmDJBollX4wUXnjGkUVuCpeTKzUQ57M4JjwEkaXDkNOivAOAXTJ_XA3d5c3eSoOyktS4SUIXfFGwAY1tvKvbo2em6cztWM7_hdQDbZDU99v3FcD4yooJxpuKPZZnLotLSFmOjBR-Bv3-wxLP0_4ZCQzafwX0gv7e47J9I0gQ__z_MY2VrG-uqgCfVjsR68H2Y-poUg0UFM4bn1kzNBz4OwSciDjFlJTg0x4E1bdrT4B0BtNY9e5maNfY6AvWUh5GvY_EAiKQ3mR8QQVMyAEL5MKRph4EDg

# user token
# export TOKEN=eyJhbGciOiJSUzI1NiIsImtpZCI6IjU0YmIyMTY1LTcxZTEtNDFhNi1hZjNlLTdkYTRhMGUxZTJjMSIsInR5cCI6IkpXVCJ9.eyJpc3MiOiJzZXJ2aWNlIHByb2plY3QiLCJzdWIiOiIzOGRjOWQ4NC0wMThiLTRhMTUtYjk1OC0wYjc4YWYxMWMzMDEiLCJleHAiOjE3ODQ4Mzc0MzgsImlhdCI6MTc1MzMwMTQzOCwiUm9sZXMiOlsiVVNFUiJdfQ.TjX_mgbpT6RuoHln6Ol_NfiM3-XXsIPzAQGQgrXtANgRULPFqiPJHIJE6noc5Egoa50ZR_qHj5wzEYa0jE64Ws0xMzGMRtrdEiugLRT8C-ixz5IsMFvEPft_M2AYCFzGl6F8z-fkEiAUyXcg6imwZ6RMfVeDXqnca__MPoznIt8EbAaihKEvjmoG3ypmtjbv5Vwv6jnaM2wSCo9IAHlIu0xc47vzSy6e8c4HabMUUyYMqKcEJd5ptw0x8w5j1TOn7iGv-YAXDe-8FSZTGug9pufVY_GRLhMnSDUs3o3FyUemCoOqKz9sdVrFNc2UrV9U6g6wyWqZhjmalDIC1PdZGQ

curl-auth:
	curl -il \
	-H "Authorization: Bearer ${TOKEN}" "http://localhost:3000/testauth"

token:
	curl -il \
	--user "admin@example.com:gophers" http://localhost:6000/auth/token/54bb2165-71e1-41a6-af3e-7da4a0e1e2c1

curl-auth2:
	curl -il \
	-H "Authorization: Bearer ${TOKEN}" "http://localhost:6000/auth/authenticate"

# ==============================================================================
# Define dependencies

GOLANG          := golang:1.24
ALPINE          := alpine:3.22
KIND            := kindest/node:v1.33.1
POSTGRES        := postgres:17.5
GRAFANA         := grafana/grafana:11.6.0
PROMETHEUS      := prom/prometheus:v3.4.0
TEMPO           := grafana/tempo:2.7.0
LOKI            := grafana/loki:3.5.0
PROMTAIL        := grafana/promtail:3.5.0

KIND_CLUSTER    := al-service-cluster
NAMESPACE       := sales-system
SALES_APP       := sales
AUTH_APP        := auth
BASE_IMAGE_NAME := localhost/mrcruz117
VERSION         := 0.0.1
SALES_IMAGE     := $(BASE_IMAGE_NAME)/$(SALES_APP):$(VERSION)
METRICS_IMAGE   := $(BASE_IMAGE_NAME)/metrics:$(VERSION)
AUTH_IMAGE      := $(BASE_IMAGE_NAME)/$(AUTH_APP):$(VERSION)

# ==============================================================================
# Building containers

build: sales auth

sales:
	docker build \
		-f zarf/docker/dockerfile.sales \
		-t $(SALES_IMAGE) \
		--build-arg BUILD_REF=$(VERSION) \
		--build-arg BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ") \
		.

auth:
	docker build \
		-f zarf/docker/dockerfile.auth \
		-t $(AUTH_IMAGE) \
		--build-arg BUILD_REF=$(VERSION) \
		--build-arg BUILD_DATE=$(date -u +"%Y-%m-%dT%H:%M:%SZ") \
		.

# ==============================================================================
# Running in k8s/kind

dev-up:
	kind create cluster \
		--image $(KIND) \
		--name $(KIND_CLUSTER) \
		--config zarf/k8s/dev/kind-config.yaml

	kubectl wait --timeout=120s --namespace=local-path-storage --for=condition=Available deployment/local-path-provisioner

	kind load docker-image $(POSTGRES) --name $(KIND_CLUSTER)

dev-down:
	kind delete cluster --name $(KIND_CLUSTER)

dev-status-all:
	kubectl get nodes -o wide
	kubectl get svc -o wide
	kubectl get pods -o wide --watch --all-namespaces

dev-status:
	watch -n 2 kubectl get pods -o wide --all-namespaces

# =========================================================

dev-load:
	kind load docker-image $(SALES_IMAGE) --name $(KIND_CLUSTER)
	kind load docker-image $(AUTH_IMAGE) --name $(KIND_CLUSTER)

dev-load-db:
	kind load docker-image $(POSTGRES) --name $(KIND_CLUSTER)
	
dev-apply:
	kustomize build zarf/k8s/dev/database | kubectl apply -f -
	kubectl rollout status --namespace=$(NAMESPACE) --watch --timeout=120s sts/database

	kustomize build zarf/k8s/dev/auth | kubectl apply -f -
	kubectl wait pods --namespace=$(NAMESPACE) --selector app=$(AUTH_APP) --timeout=120s --for=condition=Ready
	
	kustomize build zarf/k8s/dev/auth | kubectl apply -f -
	kubectl wait pods --namespace=$(NAMESPACE) --selector app=$(AUTH_APP) --timeout=120s --for=condition=Ready

dev-restart:
	kubectl rollout restart deployment $(AUTH_APP) --namespace=$(NAMESPACE)
	kubectl rollout restart deployment $(SALES_APP) --namespace=$(NAMESPACE)
	
dev-restart-auth:

dev-update: build dev-load dev-restart

dev-update-apply: build dev-load dev-apply

dev-logs:
	kubectl logs --namespace=$(NAMESPACE) -l app=$(SALES_APP) --all-containers=true -f --tail=100 --max-log-requests=6 | go run apis/tooling/logfmt/main.go -service=$(SALES_APP)

dev-logs-auth:
	kubectl logs --namespace=$(NAMESPACE) -l app=$(AUTH_APP) --all-containers=true -f --tail=100 | go run apis/tooling/logfmt/main.go

dev-describe-auth:
	kubectl describe pod --namespace=$(NAMESPACE) -l app=$(AUTH_APP)

# =========================================================

dev-describe-deployment:
	kubectl describe deployment --namespace=$(NAMESPACE) $(SALES_APP)

dev-describe-sales:
	kubectl describe pod --namespace=$(NAMESPACE) -l app=$(SALES_APP)

# ==============================================================================
# Administration

pgcli:
	pgcli postgresql://postgres:postgres@localhost
	
# ==============================================================================
# Metrics and Tracing

metrics:
	expvarmon -ports="localhost:3010" -vars="build,requests,goroutines,errors,panics,mem:memstats.HeapAlloc,mem:memstats.HeapSys,mem:memstats.Sys"

statsviz:
	open -a "Google Chrome" http://localhost:3010/debug/statsviz

# =========================================================
# Modules support

tidy:
	go mod tidy
	go mod vendor

# ==============================================================================
# Local tests

test-r:
	CGO_ENABLED=1 go test -race -count=1 ./...

test-only:
	CGO_ENABLED=0 go test -count=1 ./...

lint:
	CGO_ENABLED=0 go vet ./...
	staticcheck -checks=all ./...

vuln-check:
	govulncheck ./...

test: test-only lint vuln-check