.PHONY: build build-noaxiom install run clean test fmt vet lint check check-noaxiom

BINARY := tailboard
PREFIX := $(HOME)/.local/bin

# staticcheck 2026.2.2, run through go run so a fresh checkout needs nothing
# on PATH. Keep in step with .github/workflows/ci.yml.
STATICCHECK_VERSION := v0.8.2
STATICCHECK := go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION)

build:
	go build -o $(BINARY) .

# A file-only binary: no Axiom backend, no axiom-go SDK, no OpenTelemetry.
build-noaxiom:
	go build -tags noaxiom -o $(BINARY) .

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
	$(MAKE) check-noaxiom

# The noaxiom build vets, lints and tests clean, and links neither the
# axiom-go SDK nor OpenTelemetry.
check-noaxiom:
	go vet -tags noaxiom ./...
	$(STATICCHECK) -tags noaxiom ./...
	go test -race -tags noaxiom ./...
	@n=$$(go list -deps -tags noaxiom . | grep -c -E 'axiomhq|opentelemetry'); \
		test "$$n" -eq 0 || (echo "noaxiom build still links $$n axiom or opentelemetry packages" && exit 1)
