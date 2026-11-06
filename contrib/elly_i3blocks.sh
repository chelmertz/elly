#!/usr/bin/env bash

# Requires curl and jq

set -eo pipefail

text_or_icon="$1"

elly_url=http://localhost:9876
# where a click opens the page, e.g. http://elly.localhost:9876 for its own
# entry in the browser's history; the API calls stay on elly_url
page_url=${ELLY_PAGE_URL:-$elly_url}
# the PRs the page puts on the user, so the count matches it
prs=$(curl -sf "$elly_url/api/v0/prs?turn=you")

# left click - open the page
if [ "$BLOCK_BUTTON" = "1" ]; then
	xdg-open "$page_url/" &
	wmctrl -a firefox
fi

# show backoff multiplier if > 1
backoff_suffix=""
multiplier=$(curl -sf "$elly_url/metrics" | grep '^elly_backoff_multiplier ' | awk '{printf "%.0f", $2}') || true
if [ -n "$multiplier" ] && [ "$multiplier" -gt 1 ] 2>/dev/null; then
	backoff_suffix=" (${multiplier}x)"
fi

count=$(jq 'length' <(echo "$prs"))
if [ "$count" -gt 0 ]; then
	echo "$text_or_icon $count$backoff_suffix"
	echo "$text_or_icon $count$backoff_suffix"
	# fg role of the dotfiles bar palette (i3blocks-color is generated from
	# nix/home.nix in the dotfiles repo); plain green without that helper
	command -v i3blocks-color >/dev/null && i3blocks-color fg || echo "#00ff00"

	# right click - open each PR on the user in the browser
	if [ "$BLOCK_BUTTON" = "3" ]; then
		for u in $(jq -r '.[].Url' <(echo "$prs")); do
			xdg-open "$u" &
		done
		wmctrl -a firefox
	fi
else
	echo "$text_or_icon $count$backoff_suffix"
fi
