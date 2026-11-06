BINARY := cloudnest
AGENT_BINARY := cloudnest-agent
CMD := ./cmd/server
DIST := ./dist

.PHONY: build build-macos build-linux release clean test check integration performance network vuln

test:
	go test -race -count=1 ./...

check:
	go vet ./...
	go build ./...

integration:
	../scripts/test-integration.sh

performance:
	../scripts/test-transfer-performance.sh

network:
	../scripts/test-transfer-network.sh

vuln:
	../scripts/check-vulnerabilities.sh

build:
	mkdir -p $(DIST)
	go build -o $(DIST)/$(BINARY) $(CMD)
	go build -o $(DIST)/$(AGENT_BINARY) ./cmd/agent

build-macos:
	mkdir -p $(DIST)/macos-amd64 $(DIST)/macos-arm64
	GOOS=darwin GOARCH=amd64 go build -o $(DIST)/macos-amd64/$(BINARY) $(CMD)
	GOOS=darwin GOARCH=arm64 go build -o $(DIST)/macos-arm64/$(BINARY) $(CMD)
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build -o $(DIST)/macos-amd64/$(AGENT_BINARY) ./cmd/agent
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o $(DIST)/macos-arm64/$(AGENT_BINARY) ./cmd/agent

build-linux:
	mkdir -p $(DIST)/linux-amd64 $(DIST)/linux-arm64
	GOOS=linux GOARCH=amd64 go build -o $(DIST)/linux-amd64/$(BINARY) $(CMD)
	GOOS=linux GOARCH=arm64 go build -o $(DIST)/linux-arm64/$(BINARY) $(CMD)
	GOOS=linux GOARCH=amd64 go build -o $(DIST)/linux-amd64/$(AGENT_BINARY) ./cmd/agent
	GOOS=linux GOARCH=arm64 go build -o $(DIST)/linux-arm64/$(AGENT_BINARY) ./cmd/agent

release:
	./scripts/release.sh $(VERSION)

clean:
	rm -rf $(DIST)
