CONTROLLER_GEN_VERSION ?= v0.17.3
CONTROLLER_GEN ?= $(CURDIR)/bin/controller-gen
IMG ?= ghcr.io/moindo/cluster-api-provider-bootbox:latest

.PHONY: all
all: generate test build

$(CONTROLLER_GEN):
	GOBIN=$(CURDIR)/bin go install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

.PHONY: generate
generate: $(CONTROLLER_GEN) ## Generate deepcopy code, CRDs and RBAC.
	$(CONTROLLER_GEN) object:headerFile=hack/boilerplate.go.txt paths=./api/...
	$(CONTROLLER_GEN) crd rbac:roleName=manager-role paths=./... \
		output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac
	python3 hack/gen-provider-chart.py packages/system/cluster-api-provider-bootbox

.PHONY: test
test:
	go vet ./...
	go test ./...

.PHONY: build
build:
	CGO_ENABLED=0 go build -o bin/manager ./cmd

.PHONY: docker-build
docker-build:
	docker build -t $(IMG) .

.PHONY: manifests
manifests: ## Render the generic install.
	kubectl kustomize config/default
