test:
	go test -v ./...
# can't do go test -v -fuzz=Fuzz -fuzztime=10s ./... which errors out with:
# testing: will not fuzz, -fuzz matches more than one fuzz test: [Fuzz_WhenReviewThreadsExist_WillCountUnresponded Fuzz_LowerLoc_HigherPoints]
	./fuzz_multiple.sh

# GOPROXY is set explicitly to avoid local caching issues when determining the latest version
release:
	@version=$$(GOPROXY=https://proxy.golang.org,direct go tool gorelease | grep Suggested | cut -d' ' -f3); \
	if [ -z "$$version" ]; then \
		echo "Error: gorelease did not suggest a version"; \
		exit 1; \
	fi; \
	prev_tag=$$(git describe --tags --abbrev=0 2>/dev/null || echo ""); \
	echo "Creating release $$version (previous: $${prev_tag:-none})"; \
	if [ -n "$$prev_tag" ]; then \
		changelog=$$(git log $$prev_tag..HEAD --pretty=format:"- %s ([%h](https://github.com/chelmertz/elly/commit/%H))"); \
	else \
		changelog=$$(git log --pretty=format:"- %s ([%h](https://github.com/chelmertz/elly/commit/%H))"); \
	fi; \
	git tag -a "$$version" -m "Release $$version"; \
	git push origin "$$version"; \
	printf '## Changelog\n%s\n' "$$changelog" | gh release create "$$version" --title "$$version" --notes-file -

lint:
	pinact run --update
	zizmor .
	golangci-lint run
	hadolint Dockerfile

models:
	go get -tool github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go mod tidy
	go tool sqlc generate

# Slow checks, run daily by .github/workflows/slow-tests.yml instead of on
# every push. One worker keeps a mutation run's memory bounded. The
# thresholds live in .gremlins.yaml, because gremlins v0.6.0 silently drops
# --threshold-efficacy and --threshold-mcover (each reads as 0, which
# disables the check); that file says what they are and why.
# gremlins times each mutant against its first test run; served from the
# test cache, that run takes milliseconds and most mutants then time out.
mutate:
	go clean -testcache
	CGO_ENABLED=0 go tool gremlins unleash --workers 1 .

# The property tests with 10000 cases each instead of rapid's default 100.
PROP_PACKAGES = ./anonymise ./internal/github ./internal/notify ./internal/points ./internal/server
props-long:
	CGO_ENABLED=0 go test -run Prop $(PROP_PACKAGES) -args -rapid.checks=10000

.PHONY: test release lint models mutate props-long
