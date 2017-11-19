all: deployd

deployd: $(shell find . -name '*.go')
	go build

clean:
	rm -f deployd

.PHONY: all
.PHONY: clean
