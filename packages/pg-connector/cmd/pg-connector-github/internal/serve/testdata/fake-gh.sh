#!/bin/bash
# fake-gh: a counting stand-in for the `gh` binary, for the daemon's real-process
# tests (internal/serve, via internal/servetest). It answers the three GraphQL
# documents the daemon sends (the viewer, the membership-only search and the
# nodes(ids:) summary batch) from fixture files, answers `gh auth token`, and
# records every call, so a test can count the origin calls the daemon made.
#
# Everything lives in $FAKE_GH_DIR (required):
#
#   calls.log            appended to: one line per call, "GH_HOST=<value>" then
#                        the argv, tab separated (a tab or a newline inside an
#                        argument is written as \t or \n)
#   token                what `gh auth token` prints, and the only GH_TOKEN an
#                        `api` call accepts (anything else answers 401)
#   mode                 ok (default) | 502 | secondary | primary | 401 | notoken
#   failn                a count: the next N `api` calls answer 502, then mode applies
#   delay                seconds every `api` call sleeps before answering
#   remaining            the rateLimit remaining every answer reports (default 4900)
#   retry_after          the Retry-After seconds of mode secondary (default 3600)
#   viewer.login         the login the viewer query answers (default fixture-viewer)
#   search.json          the search answer; search.<q>.json wins when its name,
#                        the query string with every character outside
#                        [A-Za-z0-9] replaced by _, matches
#   node.<id>.json       the PullRequest node answered for node id <id> (null when absent)
#
# A fixture file may write @REMAINING@ where the remaining count belongs. The
# shim honours --include (the daemon always passes it): the status line and the
# headers come first, then a blank line, then the body.
#
# Generic by construction: no real organisation, repository or user is named.

dir="${FAKE_GH_DIR:?FAKE_GH_DIR is required}"

read_or() { # <file> <default>
  if [ -f "$dir/$1" ]; then
    tr -d '\n' <"$dir/$1"
  else
    printf '%s' "$2"
  fi
}

record() {
  local line="GH_HOST=${GH_HOST-}" a
  for a in "$@"; do
    a=${a//$'\t'/\\t}
    a=${a//$'\n'/\\n}
    line="$line"$'\t'"$a"
  done
  printf '%s\n' "$line" >>"$dir/calls.log"
}

record "$@"

mode=$(read_or mode ok)

if [ "$1" = "auth" ] && [ "$2" = "token" ]; then
  if [ "$mode" = "notoken" ]; then
    echo "To get started with GitHub CLI, please run:  gh auth login" >&2
    exit 4
  fi
  read_or token fixture-token
  echo
  exit 0
fi

if [ "$1" != "api" ]; then
  echo "fake-gh: unsupported command: $1" >&2
  exit 1
fi

include=no
doc=
q=
ids=()
while [ $# -gt 0 ]; do
  case "$1" in
  --include) include=yes ;;
  -F | -f)
    shift
    case "$1" in
    query=*) doc=${1#query=} ;;
    q=*) q=${1#q=} ;;
    ids\[\]=*) ids+=("${1#ids\[\]=}") ;;
    esac
    ;;
  esac
  shift
done

delay=$(read_or delay 0)
if [ "$delay" != "0" ]; then
  sleep "$delay"
fi

remaining=$(read_or remaining 4900)
reset="2999-01-01T00:00:00Z"

# reply <status line> <extra header lines, newline separated> <body> <stderr> <exit>
reply() {
  if [ "$include" = yes ]; then
    printf '%s\r\n' "$1"
    printf 'X-Ratelimit-Limit: 5000\r\n'
    printf 'X-Ratelimit-Remaining: %s\r\n' "$remaining"
    printf 'X-Ratelimit-Resource: graphql\r\n'
    if [ -n "$2" ]; then
      printf '%s\r\n' "$2"
    fi
    printf '\r\n'
  fi
  printf '%s\n' "$3"
  if [ -n "$4" ]; then
    echo "$4" >&2
  fi
  exit "$5"
}

failn=$(read_or failn 0)
if [ "$failn" -gt 0 ] 2>/dev/null; then
  echo $((failn - 1)) >"$dir/failn"
  mode=502
fi

want=$(read_or token fixture-token)
if [ "${GH_TOKEN-}" != "$want" ]; then
  mode=401
fi

case "$mode" in
502)
  reply "HTTP/2.0 502 Bad Gateway" "" '{"message":"Bad Gateway"}' "gh: Bad Gateway (HTTP 502)" 1
  ;;
401)
  reply "HTTP/2.0 401 Unauthorized" "" '{"message":"Bad credentials"}' "gh: Bad credentials (HTTP 401)" 1
  ;;
secondary)
  reply "HTTP/2.0 403 Forbidden" "Retry-After: $(read_or retry_after 3600)" \
    '{"message":"You have exceeded a secondary rate limit."}' \
    "gh: You have exceeded a secondary rate limit. Please wait a few minutes before you try again. (HTTP 403)" 1
  ;;
primary)
  remaining=0
  reply "HTTP/2.0 403 Forbidden" "" '{"message":"API rate limit exceeded"}' "gh: API rate limit exceeded (HTTP 403)" 1
  ;;
esac

rate="\"rateLimit\":{\"cost\":1,\"remaining\":$remaining,\"resetAt\":\"$reset\"}"

case "$doc" in
*"viewer {"*)
  login=$(read_or viewer.login fixture-viewer)
  reply "HTTP/2.0 200 OK" "" "{\"data\":{\"viewer\":{\"login\":\"$login\"},$rate}}" "" 0
  ;;
*"search("*)
  key=$(printf '%s' "$q" | tr -c 'A-Za-z0-9' '_')
  file="$dir/search.$key.json"
  [ -f "$file" ] || file="$dir/search.json"
  if [ ! -f "$file" ]; then
    reply "HTTP/2.0 200 OK" "" "{\"data\":{$rate,\"search\":{\"issueCount\":0,\"pageInfo\":{\"hasNextPage\":false,\"endCursor\":\"\"},\"nodes\":[]}}}" "" 0
  fi
  body=$(sed "s/@REMAINING@/$remaining/g" "$file")
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
*"nodes(ids"*)
  nodes=
  for id in ${ids[@]+"${ids[@]}"}; do
    f="$dir/node.$id.json"
    if [ -f "$f" ]; then
      n=$(cat "$f")
    else
      n=null
    fi
    nodes="${nodes:+$nodes,}$n"
  done
  body=$(printf '{"data":{%s,"nodes":[%s]}}' "$rate" "$nodes" | sed "s/@REMAINING@/$remaining/g")
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
*)
  echo "fake-gh: unrecognized GraphQL document" >&2
  exit 1
  ;;
esac
