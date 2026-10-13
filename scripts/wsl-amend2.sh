#!/usr/bin/env bash
set -uo pipefail
cd /mnt/c/Users/Administrator/Desktop/mmx || exit 1
git -c core.autocrlf=false commit --amend -q -F scripts/_msg.txt
rm -f scripts/_msg.txt
git --no-pager log -1 --format=%B | head -6
