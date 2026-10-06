.PHONY: build run clean test psps fraud-venv fraud-train fraud-serve fraud-bench
build:
	@mkdir -p bin
	go build -o bin/server ./cmd/server
	@echo "built bin/server"
run: build
	./bin/server -addr :8090
clean:
	rm -rf bin

test:
	go vet ./...
	go test ./...

# --- SmartRoute gateways (mock PSPs, ports 9001-9003). Run in a 2nd terminal. ---
psps:
	@mkdir -p bin
	go build -o bin/psp ./cmd/psp
	@trap 'kill 0' INT TERM EXIT; \
	bin/psp -addr :9001 -name psp-a -success 0.90 -latency 40  -cost 1.0 & \
	bin/psp -addr :9002 -name psp-b -success 0.98 -latency 90  -cost 2.0 & \
	bin/psp -addr :9003 -name psp-c -success 0.95 -latency 150 -cost 1.5 & \
	wait

# --- Graph fraud detection (Python). Ports: fraud service 8100. ---
fraud-venv:
	python3 -m venv fraud/.venv && fraud/.venv/bin/pip install -r fraud/requirements-train.txt
	python3 -m venv fraud/.venv-serve && fraud/.venv-serve/bin/pip install -r fraud/serve/requirements-dev.txt
fraud-train:
	cd fraud && .venv/bin/python -W ignore train.py
fraud-serve:
	cd fraud/serve && ../.venv-serve/bin/uvicorn app:app --port 8100
fraud-bench:
	cd fraud && .venv-serve/bin/python bench.py
