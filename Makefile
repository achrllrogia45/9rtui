BINARY=9rtui

.PHONY: test build build-linux build-windows clean install-local

test:
	go test ./...

build: build-linux

build-linux:
	go build -o $(BINARY) .

build-windows:
	GOOS=windows GOARCH=amd64 go build -o $(BINARY).exe .

install-local: build-linux
	mkdir -p $(HOME)/.9rtui/scripts $(HOME)/.9rtui/.accounts $(HOME)/.9rtui/.tui-logs/full-backups
	cp -f $(BINARY) $(HOME)/.9rtui/$(BINARY)
	rm -rf $(HOME)/.9rtui/scripts
	cp -R scripts $(HOME)/.9rtui/scripts

clean:
	rm -f $(BINARY) $(BINARY).exe
