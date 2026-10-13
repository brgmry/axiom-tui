.PHONY: build install run clean test fmt vet lint check

BINARY := tailboard
PREFIX := $(HOME)/.local/bin

# staticcheck 2026.2.2, run through go run so a fresh checkout needs nothing
# on PATH. Keep in step with .github/workflows/ci.yml.
STATICCHECK_VERSION := v0.8.2
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

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
	$(STATICCHECK) ./...

check:
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo "gofmt: files need formatting" && exit 1)
	go vet ./...
	$(STATICCHECK) ./...
	go test -race ./...
