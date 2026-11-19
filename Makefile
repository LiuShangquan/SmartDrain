SHELL := /usr/bin/env bash
.SHELLFLAGS := -eu -o pipefail -c

IMAGE ?= smartdrain:dev
CONTEXT ?= kind-smartdrain-dev
KUBECONFIG ?= $(CURDIR)/.smartdrain.kubeconfig
export KUBECONFIG
CONTROLLER_GEN_VERSION := v0.20.0

.PHONY: generate manifests fmt vet test build install deploy undeploy e2e
generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) object:headerFile=hack/boilerplate.go.txt paths=./api/v1alpha1

manifests:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) crd paths=./api/v1alpha1 output:crd:artifacts:config=config/crd/bases
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION) rbac:roleName=smartdrain-manager-role paths=./internal/controller output:rbac:artifacts:config=config/rbac

fmt:
	gofmt -w api cmd internal test

vet:
	go vet ./...

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -o bin/manager ./cmd

install: manifests
	kubectl --context $(CONTEXT) apply -k config/crd

deploy:
	kubectl --context $(CONTEXT) apply -k config/default

undeploy:
	@test -z "$$(kubectl --context $(CONTEXT) get nodemaintenances -o jsonpath='{.items[*].metadata.finalizers}')" || { echo 'Restore all active tasks before uninstalling'; exit 1; }
	kubectl --context $(CONTEXT) delete -k config/default --ignore-not-found

e2e:
	SMARTDRAIN_E2E=1 go test -v -count=1 -timeout=30m ./test/e2e
