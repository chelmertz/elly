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

count=$(jq 'length' <(echo "$prs"))
if [ "$count" -gt 0 ]; then
	echo "$text_or_icon $count"
	echo "$text_or_icon $count"
	echo "#00ff00"

	# right click - open all PRs in browser, one by one
	if [ "$BLOCK_BUTTON" = "3" ]; then
		for u in $(jq -r '.[].Url' <(echo "$prs")); do
			xdg-open "$u" &
		done
		wmctrl -a firefox
	fi
else
	echo "$text_or_icon $count"
fi
