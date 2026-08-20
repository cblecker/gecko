.PHONY: generate build test

generate:
	@echo "Generating public APIs, schemas, and deepcopy methods..."
	@go run ./cmd/orlop-gen
	@echo "Done!"

build:
	go build ./...

test:
	go test ./...
# Local server for testing (memory backend)
.PHONY: server-start server-stop server-logs server-test

server-start:
	@echo "Starting platform-api-server (memory backend)..."
	@go build -o /tmp/platform-api-server ./cmd/platform-api-server
	@env -u SPANNER_DATABASE -u DB_HOST nohup /tmp/platform-api-server --disable-auth --address=127.0.0.1 --private-port=18080 --public-port=18081 > /tmp/platform-api-server.log 2>&1 &
	@sleep 2
	@if pgrep -f "platform-api-server --disable-auth" > /dev/null; then \
		echo "Server started (PID $$(pgrep -f 'platform-api-server --disable-auth'))"; \
		echo "Private API: https://127.0.0.1:18080 (skip TLS verification)"; \
		echo "Public API:  http://127.0.0.1:18081"; \
	else \
		echo "Server failed to start, check /tmp/platform-api-server.log"; \
		exit 1; \
	fi

server-stop:
	@echo "Stopping platform-api-server..."
	@pkill -f "platform-api-server --disable-auth" || echo "Server not running"
	@sleep 1

server-logs:
	@tail -f /tmp/platform-api-server.log

server-test:
	@echo "Testing server endpoints..."
	@echo "1. Public API discovery:"
	@curl -s http://127.0.0.1:18081/apis/gcp.managed.openshift.io/v1 | jq -r '.resources[].name'
	@echo "\n2. Create test cluster:"
	@echo '{"apiVersion":"gcp.managed.openshift.io/v1","kind":"Cluster","metadata":{"name":"test1","namespace":"test"},"spec":{"platform":{"type":"GCP","gcp":{"projectID":"test","region":"us-central1","workloadIdentity":{"poolID":"pool","projectNumber":"123","providerID":"provider"}}},"release":{"version":"4.19.0","channelGroup":"stable"},"networking":{}}}' | \
		curl -s -X POST http://127.0.0.1:18081/apis/gcp.managed.openshift.io/v1/namespaces/test/clusters -H "Content-Type: application/json" -d @- | jq -r '.metadata.name // .message'
	@echo "\n3. List with kubectl (Table format):"
	@command -v kubectl >/dev/null && kubectl --server=http://127.0.0.1:18081 get clusters -n test || echo "kubectl not installed"
