GLINT_BIN ?= $(HOME)/bin/glint

test: ## Юнит-тесты
	go test ./...

vet: ## go vet
	go vet ./...

glint: ## Glint-анализ
	@$(GLINT_BIN) check .

smoke: vet test ## Быстрая проверка перед коммитом
