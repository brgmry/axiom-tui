.PHONY: build install run clean test

BINARY := axiom-tui
PREFIX := $(HOME)/.local/bin

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(PREFIX)
	cp $(BINARY) $(PREFIX)/$(BINARY)
	@echo "installed → $(PREFIX)/$(BINARY)"

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)

test:
	go test ./...

fmt:
	gofmt -w -s .

vet:
	go vet ./...
