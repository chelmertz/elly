test:
	go test -v ./...
# can't do go test -v -fuzz=Fuzz -fuzztime=10s ./... which errors out with:
# testing: will not fuzz, -fuzz matches more than one fuzz test: [Fuzz_WhenReviewThreadsExist_WillCountUnresponded Fuzz_LowerLoc_HigherPoints]
	./fuzz_multiple.sh

release:
	@version=$$(go tool gorelease | grep Suggested | cut -d' ' -f3); \
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
	gh release create "$$version" --title "$$version" --notes "## Changelog"$$'\n'"$$changelog"

models:
	go get -tool github.com/sqlc-dev/sqlc/cmd/sqlc@latest
	go mod tidy
	go tool sqlc generate

.PHONY: test release models
