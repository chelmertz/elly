#!/usr/bin/env bash

# Requires curl and jq

set -euo pipefail

text_or_icon="$1"

prs=$(curl -q 'http://localhost:9876/api/v0/prs?minPoints=0')
count=$(jq 'length' <(echo "$prs"))
if [ "$count" -gt 0 ]; then
	echo "$text_or_icon $count"
	echo "$text_or_icon $count"
	echo "#00ff00"

	if [ "$BLOCK_BUTTON" -eq 1 ]; then
		for u in $(jq -r '.[].Url' <(echo "$prs")); do
			xdg-open "$u" &
			wmctrl -a chrome
		done
	fi
else
	echo "$text_or_icon $count"
fi
