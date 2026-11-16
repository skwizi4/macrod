BINARY_DIR := bin
CONFIG     ?= config.yaml
LINUX_ARCH ?= arm64

.PHONY: build test test-race-all lint check run backfill export-dry status calendar serve site build-linux docker-build up down logs doctor record clean tools

build:
	go build -o $(BINARY_DIR)/macrod ./cmd/macrod
	go build -o $(BINARY_DIR)/macroctl ./cmd/macroctl

# Пакет internal/export — однопоточный рендер поверх SQLite (modernc, C → Go): под -race он в ~30 раз
# медленнее (~750 с CPU против ~60 с у всего набора без -race) и греет ноутбук. Поэтому export — без -race,
# остальные пакеты (writer-горутина store, планировщик, сбор, доставка) — с -race.
# Полный прогон с -race — make test-race-all (перед выкладкой или после правок конкурентного кода в export).
test:
	go test -count=1 ./internal/export/
	go test -race -count=1 $$(go list ./... | grep -v '/internal/export$$')

test-race-all:
	go test -race -count=1 ./...

lint:
	go vet ./...
	staticcheck ./...

check: lint test
	@echo "✅ check passed"

run: build
	./$(BINARY_DIR)/macrod --config $(CONFIG)

backfill: build
	./$(BINARY_DIR)/macroctl --config $(CONFIG) backfill --source fred --since 2000-01-01

export-dry: build
	./$(BINARY_DIR)/macroctl --config $(CONFIG) export --region all --dry-run

serve: build
	./$(BINARY_DIR)/macroctl --config $(CONFIG) serve

site: build
	./$(BINARY_DIR)/macroctl --config $(CONFIG) site --out site --demo

status: build
	./$(BINARY_DIR)/macroctl --config $(CONFIG) status

calendar: build
	./$(BINARY_DIR)/macroctl --config $(CONFIG) calendar --days 14

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -o $(BINARY_DIR)/linux/macrod ./cmd/macrod
	CGO_ENABLED=0 GOOS=linux GOARCH=$(LINUX_ARCH) go build -o $(BINARY_DIR)/linux/macroctl ./cmd/macroctl

# Docker (ТЗ §21.1): образ и демон через docker compose
docker-build:
	docker compose build

up:
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f --tail=100

doctor:
	docker compose exec macrod macroctl --config /app/config.yaml doctor

# Перезаписать фикстуры в testdata/ реальными ответами API (сеть нужна, только вручную)
record:
	go run ./cmd/macroctl --config $(CONFIG) record-fixtures

tools:
	go install honnef.co/go/tools/cmd/staticcheck@latest

clean:
	rm -rf $(BINARY_DIR)
