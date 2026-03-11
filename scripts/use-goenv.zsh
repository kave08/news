#!/usr/bin/env zsh

setopt errexit nounset pipefail

script_path="${(%):-%x}"
repo_root="$(cd "$(dirname "$script_path")/.." && pwd)"
export GOENV="$repo_root/.goenv"

print "Repo-local Go env loaded."
print "GOENV=$GOENV"
print "You can now use plain 'go ...' in this shell while you stay in this repo."
