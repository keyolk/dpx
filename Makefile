VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
BIN := bin/dpx

.PHONY: build run install clean tidy test vet

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) .

run: build
	$(BIN)

install: build
	rm -f ~/.local/bin/dpx
	cp $(BIN) ~/.local/bin/dpx

clean:
	rm -rf bin/

test:
	go test ./...

vet:
	go vet ./...

tidy:
	go mod tidy
