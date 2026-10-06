.PHONY: build run clean
build:
	@mkdir -p bin
	go build -o bin/server ./cmd/server
	@echo "built bin/server"
run: build
	./bin/server -addr :8090
clean:
	rm -rf bin
