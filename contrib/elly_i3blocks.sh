#!/usr/bin/env bash

# Requires curl and jq

set -eo pipefail

text_or_icon="$1"

elly_url=http://localhost:9876
prs=$(curl -q "$elly_url/api/v0/prs?minPoints=1")

# left click - open elly in browser
if [ "$BLOCK_BUTTON" = "1" ]; then
	xdg-open "$elly_url" &
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
	echo "#00ff00"

	# right click - open all PRs in browser, one by one
	if [ "$BLOCK_BUTTON" = "3" ]; then
		for u in $(jq -r '.[].Url' <(echo "$prs")); do
			xdg-open "$u" &
		done
		wmctrl -a firefox
	fi
else
	echo "$text_or_icon $count$backoff_suffix"
fi
