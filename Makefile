test:
	go test -v ./...
# can't do go test -v -fuzz=Fuzz -fuzztime=10s ./... which errors out with:
# testing: will not fuzz, -fuzz matches more than one fuzz test: [Fuzz_WhenReviewThreadsExist_WillCountUnresponded Fuzz_LowerLoc_HigherPoints]
	./fuzz_multiple.sh

tag:
	version=$(shell gorelease | grep Suggested | cut -d' ' -f3); \
	git tag -a $$version;

models:
	which sqlc || go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	sqlc generate

.PHONY: test tag models
