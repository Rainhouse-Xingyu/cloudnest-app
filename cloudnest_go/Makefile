BINARY := cloudnest
CMD := ./cmd/server
DIST := ./dist

.PHONY: build build-macos build-linux release clean test check integration performance network ai-model vuln

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

ai-model:
	../scripts/test-ai-index-model.sh

vuln:
	../scripts/check-vulnerabilities.sh

build:
	mkdir -p $(DIST)
	go build -o $(DIST)/$(BINARY) $(CMD)

build-macos:
	mkdir -p $(DIST)/macos-amd64 $(DIST)/macos-arm64
	GOOS=darwin GOARCH=amd64 go build -o $(DIST)/macos-amd64/$(BINARY) $(CMD)
	GOOS=darwin GOARCH=arm64 go build -o $(DIST)/macos-arm64/$(BINARY) $(CMD)

build-linux:
	mkdir -p $(DIST)/linux-amd64 $(DIST)/linux-arm64
	GOOS=linux GOARCH=amd64 go build -o $(DIST)/linux-amd64/$(BINARY) $(CMD)
	GOOS=linux GOARCH=arm64 go build -o $(DIST)/linux-arm64/$(BINARY) $(CMD)

release:
	./scripts/release.sh $(VERSION)

clean:
	rm -rf $(DIST)
