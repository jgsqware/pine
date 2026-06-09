.PHONY: build run tui demo test docker website

build:
	go build -o pine ./cmd/pine

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
