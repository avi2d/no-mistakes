#!/usr/bin/env bash
# Prints, one per line, the packages whose tests the diff from the merge base
# of BASE and HEAD reaches, or ./... when go.mod or go.sum changed. Prints
# nothing when the diff reaches no package. Runs from the module root.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 BASE HEAD" >&2
  exit 2
fi

merge_base=$(git merge-base "$1" "$2")
# Without --no-renames a moved file lists only its new path, and the package
# it left would go untested.
changed=$(git diff --name-only --no-renames "$merge_base" "$2")

if grep -qxE 'go\.(mod|sum)' <<<"$changed"; then
  echo ./...
  exit 0
fi

module_dir=$(go list -m -f '{{.Dir}}')

# A changed file belongs to the package in its nearest enclosing directory,
# so a testdata or embedded file reaches the package that reads it.
changed_packages=$(
  go list -f '{{.Dir}} {{.ImportPath}}' ./... | awk -v root="$module_dir" -v changed="$changed" '
    {
      dir = substr($1, length(root) + 2)
      package_of[dir] = $2
    }
    END {
      n = split(changed, files, "\n")
      for (i = 1; i <= n; i++) {
        dir = files[i]
        while (dir != "") {
          sub(/\/?[^\/]*$/, "", dir)
          if (dir in package_of) {
            print package_of[dir]
            break
          }
        }
      }
    }' | sort -u
)

if [ -z "$changed_packages" ]; then
  exit 0
fi

# Each P.test entry lists every package linked into P's test binary, its test
# imports included; a variant built for that binary carries a " [P.test]" suffix.
go list -test -f '{{.ImportPath}} {{join .Deps " "}}' ./... | awk -v changed="$changed_packages" '
  BEGIN {
    n = split(changed, list, "\n")
    for (i = 1; i <= n; i++) {
      is_changed[list[i]] = 1
      print list[i]
    }
  }
  $1 ~ /\.test$/ {
    package = substr($1, 1, length($1) - length(".test"))
    for (i = 2; i <= NF; i++) {
      if ($i in is_changed) {
        print package
        break
      }
    }
  }' | sort -u
