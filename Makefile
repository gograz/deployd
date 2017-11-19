all: deployd

deployd: $(shell find . -name '*.go')
	go build

deployd_linux: $(shell find . -name '*.go')
	GOOS=linux GOARCH=amd64 go build -o deployd_linux

clean:
	rm -f deployd deployd_linux

linux: deployd_linux

.PHONY: all
.PHONY: clean
.PHONY: linux
