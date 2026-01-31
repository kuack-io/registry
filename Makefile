.PHONY: build test coverage clean install uninstall fmt fmt-fix vet lint sec check dependencies package

# exclude testutil package from tests
PKGS := $(shell go list ./... | grep -v '/pkg/testutil$$')

default: build

# CGO_ENABLED=0:	build a pure go binary with no dependencies on external C libraries (like glibc)
# GOOS=linux: 		create executable for linux kernel
# GOARCH=amd64: 	the app is for amd64 architecture
# -trimpath: 		remove full file paths during compilation, makes the build reproducible
# -ldflags='-s -w':	tell linker to omit DWARF and symbol table (makes binary smaller)
build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o kuack-registry

test:
	go test -coverprofile=coverage.out $(PKGS)

coverage:
	go tool cover -html=coverage.out -o coverage.html

clean:
	rm -f kuack-registry

clear: clean

fmt:
	go fmt ./...

fmt-fix:
	gofmt -w .

vet:
	go vet ./...

lint:
	golangci-lint run

sec:
	gosec ./...

check: fmt vet lint sec test

dependencies:
	go mod tidy

package: build
	sudo docker build -t kuack-registry .
