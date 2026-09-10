#!/bin/sh
set -eu

allowed_branch() {
	case "$1" in
		feat/*|fix/*|chore/*|docs/*|test/*|refactor/*|perf/*|build/*|ci/*|revert/*|hotfix/*|main|dev) return 0 ;;
		*) return 1 ;;
	esac
}

require_allowed_branch() {
	branch=$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)
	if [ -z "$branch" ] || ! allowed_branch "$branch"; then
		echo "Git branch must be main, dev, or <type>/<name> (feat, fix, chore, docs, test, refactor, perf, build, ci, revert, hotfix)." >&2
		exit 1
	fi
}

require_global_human_identity() {
	global_name=$(git config --global --get user.name || true)
	global_email=$(git config --global --get user.email || true)
	local_name=$(git config --local --get user.name || true)
	local_email=$(git config --local --get user.email || true)
	if [ -z "$global_name" ] || [ -z "$global_email" ] || [ -n "$local_name" ] || [ -n "$local_email" ]; then
		echo "Commits must use the global human Git identity; local user.name/user.email overrides are forbidden." >&2
		exit 1
	fi
	for ident_kind in GIT_AUTHOR_IDENT GIT_COMMITTER_IDENT; do
		ident=$(git var "$ident_kind")
		ident_name=$(printf '%s\n' "$ident" | sed 's/ <.*//')
		ident_email=$(printf '%s\n' "$ident" | sed -n 's/.*<\([^>]*\)>.*/\1/p')
		if [ "$ident_name" != "$global_name" ] || [ "$ident_email" != "$global_email" ]; then
			echo "Author and committer must match the global human Git identity." >&2
			exit 1
		fi
	done
}
