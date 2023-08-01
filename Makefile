test:
	go test -v -fuzz=Fuzz -fuzztime=10s ./...

tag:
	version=$(shell gorelease | grep Suggested | cut -d' ' -f3); \
	git tag -a $$version;

.PHONY: test tag
