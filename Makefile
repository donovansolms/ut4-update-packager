#
# A simple Makefile to easily build, test and run the code
#

.PHONY: default build fmt lint run run_race test clean vet docker_build docker_run docker_clean

APP_NAME := ut4-update-packager

default: build

build:
	go build -o ./bin/${APP_NAME} ./src/*.go

run: build
	PACKAGER_RELEASE_FEED_URL="http://update.donovansolms.local/temp/utfeed.rss" \
	PACKAGER_DATABASE_HOST=127.0.0.1 \
	PACKAGER_DATABASE_PORT=3306 \
	PACKAGER_DATABASE_NAME=unattended \
	PACKAGER_DATABASE_USER=root \
	PACKAGER_DATABASE_PASSWORD=root \
	PACKAGER_WORKING_DIR="./temp/working" \
	PACKAGER_RELEASE_DIR="./temp/releases" \
	./bin/${APP_NAME}

# http://golang.org/cmd/go/#hdr-Run_gofmt_on_package_sources
fmt:
	go fmt ./...

test:
	go test ./...

test_cover:
	go test ./ -v -cover -covermode=count -coverprofile=./coverage.out

clean:
	rm ./bin/*
