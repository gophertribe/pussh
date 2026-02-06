.PHONY: build install-docker-plugin

build:
	GO111MODULE=on go build -o ./bin/docker-pussh ./cmd/pussh

install-docker-plugin: build
	mkdir -p ~/.docker/cli-plugins
	cp ./bin/docker-pussh ~/.docker/cli-plugins/docker-pussh

