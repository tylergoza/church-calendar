BINARY := church-calendar
LDFLAGS := -s -w

.PHONY: dev run test vet build build-linux build-linux-arm provision deploy clean

# Serve templates/static from disk: edit HTML/JS/CSS and just refresh.
# Port 8120, so the tracker (8080), planner (8090), User Management (8100)
# and event planner (8110) can run alongside.
dev:
	DEV=1 go run . -addr 127.0.0.1:8120

run: build
	./bin/$(BINARY)

test:
	go vet ./...
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) .

# Digital Ocean droplets / most home-lab servers
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .

# Raspberry Pi 4/5 and other ARM boxes
build-linux-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .

# DigitalOcean with Ansible (README: Deploying). Run through the Keychain
# helpers, e.g. `with-do with-um make deploy`.
provision:
	cd deploy/ansible && ansible-playbook provision.yml

deploy:
	cd deploy/ansible && ansible-playbook deploy.yml

clean:
	rm -rf bin
