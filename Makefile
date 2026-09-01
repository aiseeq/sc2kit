GLINT_BIN ?= $(HOME)/bin/glint

generate: ## Перегенерация api/ из proto/ (protoc + protoc-gen-go; версия протокола — proto/UPSTREAM)
	protoc -I proto --go_out=. --go_opt=module=github.com/aiseeq/sc2kit \
		$(foreach f,$(notdir $(wildcard proto/s2clientprotocol/*.proto)),--go_opt=Ms2clientprotocol/$(f)=github.com/aiseeq/sc2kit/api) \
		proto/s2clientprotocol/*.proto

test: ## Юнит-тесты
	go test ./...

vet: ## go vet
	go vet ./...

glint: ## Glint-анализ
	@$(GLINT_BIN) check .

smoke: vet test glint ## Быстрая проверка перед коммитом
