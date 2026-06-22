.PHONY: build install local run tui demo test docker website

build:
	go build -o pine ./cmd/pine

# Install the pine binary into $GOBIN (or $GOPATH/bin, usually ~/go/bin).
# Make sure that directory is on your PATH, then run `pine .` anywhere.
install:
	go install ./cmd/pine

# Run Pine locally against the current directory (no Docker, no demo).
local: build
	./pine .

run: build
	./pine serve --demo --data .pine

tui: build
	./pine tui --demo --data .pine

demo: run

test:
	go vet ./...
	go test ./...

docker:
	docker compose up -d --build

website:
	@echo "Open website/index.html or serve it:"
	python3 -m http.server -d website 8080
