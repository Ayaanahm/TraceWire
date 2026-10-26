SERVICES := gateway orders inventory
VERSION  ?= v0.2.0

.PHONY: test lint images compose-up compose-down up down bench scan

test:            ## unit + in-process integration tests
	go test -race -count=1 -coverpkg=./internal/... ./...

lint:
	gofmt -l . | (! grep .) && go vet ./...
	terraform -chdir=terraform fmt -check -recursive

images:          ## build all service images
	for s in $(SERVICES) loadgen; do docker build --build-arg SERVICE=$$s --build-arg VERSION=$(VERSION) -t tracewire-$$s:$(VERSION) -t tracewire-$$s:latest . ; done

compose-up:      ## local stack: services + OTel + Jaeger + Prometheus + Grafana
	docker compose up -d --build
compose-down:
	docker compose down

up: images       ## kind cluster + kube-prometheus-stack + app via Terraform
	terraform -chdir=terraform init -input=false
	terraform -chdir=terraform apply -auto-approve -var image_tag=$(VERSION)
down:
	terraform -chdir=terraform destroy -auto-approve

bench:           ## histogram-accuracy benchmark (needs compose-up)
	bench/run.sh 120s 150

scan:            ## Trivy scan of every image
	for s in $(SERVICES); do docker run --rm -v /var/run/docker.sock:/var/run/docker.sock aquasec/trivy:0.57.1 image --severity HIGH,CRITICAL --ignore-unfixed tracewire-$$s:$(VERSION); done
