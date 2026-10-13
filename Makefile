.PHONY: build install run clean test fmt vet lint check

BINARY := tailboard
PREFIX := $(HOME)/.local/bin

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY)
	@echo "installed to $(PREFIX)/$(BINARY)"

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)

test:
	go test -race ./...

fmt:
	gofmt -w -s .

vet:
	go vet ./...

lint:
	staticcheck ./...

check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt: files need formatting" && exit 1)
	go vet ./...
	staticcheck ./...
	go test -race ./...
