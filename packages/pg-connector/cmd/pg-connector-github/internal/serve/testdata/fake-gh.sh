#!/bin/bash
# fake-gh: a counting stand-in for the `gh` binary, for the daemon's real-process
# tests (internal/serve, via internal/servetest). It answers the GraphQL
# documents and REST requests the daemon sends from fixture files, answers `gh
# auth token`, and records every call, so a test can count the origin calls the
# daemon made and read what a write sent.
#
# Everything lives in $FAKE_GH_DIR (required):
#
#   calls.log            appended to: one line per call, "GH_HOST=<value>" then
#                        the argv, tab separated (a tab or a newline inside an
#                        argument is written as \t or \n)
#   stdin.log            appended to for a call that read its standard input
#                        (a write): "<kind>", a tab, the payload with each
#                        newline written as \n
#   token                what `gh auth token` prints, and the only GH_TOKEN an
#                        `api` call accepts (anything else answers 401)
#   mode                 ok (default) | 502 | secondary | primary | 401 | notoken
#   failn                a count: the next N `api` calls answer 502, then mode applies
#   delay                seconds every `api` call sleeps before answering
#   remaining            the rateLimit remaining every answer reports (default 4900)
#   retry_after          the Retry-After seconds of mode secondary (default 3600)
#   viewer.login         the login the viewer query answers (default fixture-viewer)
#
# A call is classified into a KIND, and the kind picks the fixture:
#
#   viewer               the viewer query                       (viewer.login)
#   search               the membership-only search             search.<q>.json, search.json
#   summary              the summary nodes(ids:) batch          node.<id>.json (null when absent)
#   detail               the detail nodes(ids:) batch           detail.<id>.json (null when absent)
#   threads              the review threads page                threads.<o>__<r>__<n>.json, threads.json
#   issue_comments       the issue comments page                issue_comments.<o>__<r>__<n>.json, ...
#   reviews              the reviews page                       reviews.<o>__<r>__<n>.json, ...
#   files, commits       the files / commits page               files.<o>__<r>__<n>.json, ...
#   pending              the pending review read                pending.<o>__<r>__<n>.json, pending.json
#   write_items          a review write document (stdin)        write_items.json
#   update_body          the review body update (stdin)         update_body.json
#   create_review        POST repos/<o>/<r>/pulls/<n>/reviews   create_review.json
#   runs                 GET repos/<o>/<r>/actions/runs?branch= runs.<o>__<r>__<branch>.json, runs.json
#   jobs                 GET .../runs/<id>/attempts/<n>/jobs    jobs.<id>.json, jobs.json
#   rerun                POST .../runs/<id>/rerun-failed-jobs   rerun.json (default: 201, no body)
#
# (<o>, <r>, <n> and <branch> are written with every character outside
# [A-Za-z0-9] replaced by _.) A `gh search prs ...` call answers search_prs.json
# (default []), and `gh run view --log ...` answers run.log.
#
# Controls, per kind K (any may be absent):
#
#   fail.K               an HTTP status the kind answers instead of its fixture
#                        ("502"; "502 3" for the next three calls only)
#   fail.K.batch         the same, but only for a call that carries more than one id
#   block.K              hold the next N calls of the kind (N is the file's content,
#                        default 1) after their answer is decided and before it is
#                        sent, until release.K exists (at most 60 s); the answer is
#                        the one the fixtures gave when the call arrived. Claiming a
#                        slot is atomic, so exactly N calls are held however many
#                        arrive together; each held call appends a line to held.K
#                        the moment it is holding
#   after.K              lines "<src> <dst>": once a call of kind K has been
#                        answered, copy fixture <src> over <dst>, so a write
#                        changes what the next read sees
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

# sanitize <text>: every character outside [A-Za-z0-9] becomes an underscore.
sanitize() { printf '%s' "$1" | tr -c 'A-Za-z0-9' '_'; }

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

want=$(read_or token fixture-token)

if [ "$1" = "search" ] && [ "$2" = "prs" ]; then
  if [ "${GH_TOKEN-}" != "$want" ]; then
    echo "gh: Bad credentials (HTTP 401)" >&2
    exit 1
  fi
  if [ -f "$dir/search_prs.json" ]; then cat "$dir/search_prs.json"; else echo '[]'; fi
  exit 0
fi

if [ "$1" = "run" ] && [ "$2" = "view" ]; then
  if [ "${GH_TOKEN-}" != "$want" ]; then
    echo "gh: Bad credentials (HTTP 401)" >&2
    exit 1
  fi
  if [ -f "$dir/run.log" ]; then cat "$dir/run.log"; else echo "fixture log line"; fi
  exit 0
fi

if [ "$1" != "api" ]; then
  echo "fake-gh: unsupported command: $1" >&2
  exit 1
fi

include=no
method=GET
endpoint=
doc=
q=
owner=
name=
number=
useinput=no
ids=()
shift
while [ $# -gt 0 ]; do
  case "$1" in
  --include) include=yes ;;
  --method)
    shift
    method=$1
    ;;
  --input)
    shift
    useinput=yes
    ;;
  -F | -f)
    shift
    case "$1" in
    query=*) doc=${1#query=} ;;
    q=*) q=${1#q=} ;;
    ids\[\]=*) ids+=("${1#ids\[\]=}") ;;
    owner=*) owner=${1#owner=} ;;
    name=*) name=${1#name=} ;;
    number=*) number=${1#number=} ;;
    esac
    ;;
  -H | --header | --jq | -q | --cache | --template | -t | --hostname)
    shift
    ;;
  -*) ;;
  *)
    if [ -z "$endpoint" ]; then endpoint=$1; fi
    ;;
  esac
  shift
done

stdin=
if [ "$useinput" = yes ]; then
  stdin=$(cat)
  doc=$stdin
fi

# ---- classify ---------------------------------------------------------------

kind=other
resource=core
if [ "$endpoint" = graphql ]; then
  resource=graphql
  case "$doc" in
  *"pending: reviews("*) kind=pending ;;
  *"updatePullRequestReview("*) kind=update_body ;;
  *"addPullRequestReview"*) kind=write_items ;;
  *"mergeStateStatus"*) kind=detail ;;
  *"viewer { login }"*) kind=viewer ;;
  *"search("*) kind=search ;;
  *"nodes(ids"*) kind=summary ;;
  *"reviewThreads(first: 100, after"*) kind=threads ;;
  *"node(id: \$threadId)"*) kind=thread_comments ;;
  *"reviews(first: 100, after: \$after) {"*) kind=reviews ;;
  *"comments(first: 100, after: \$after) {"*) kind=issue_comments ;;
  *"files(first: 100"*) kind=files ;;
  *"commits(first: 100"*) kind=commits ;;
  esac
else
  case "$endpoint" in
  */actions/runs/*/rerun-failed-jobs) kind=rerun ;;
  */attempts/*/jobs*) kind="jobs" ;;
  */actions/runs\?*) kind=runs ;;
  repos/*/pulls/*/reviews)
    if [ "$method" = POST ]; then kind=create_review; fi
    ;;
  esac
fi

if [ -n "$stdin" ]; then
  esc=${stdin//$'\n'/\\n}
  printf '%s\t%s\n' "$kind" "$esc" >>"$dir/stdin.log"
fi

# The per-PR key of a repository query, and of a REST endpoint.
prkey="$(sanitize "$owner")__$(sanitize "$name")__$(sanitize "$number")"
restrepo=
restrun=
restbranch=
case "$endpoint" in
repos/*)
  rest=${endpoint#repos/}
  restrepo="$(sanitize "${rest%%/*}")"
  rest=${rest#*/}
  restrepo="${restrepo}__$(sanitize "${rest%%/*}")"
  case "$endpoint" in
  */actions/runs/*)
    rest=${endpoint#*/actions/runs/}
    restrun=${rest%%/*}
    ;;
  esac
  case "$endpoint" in
  *branch=*)
    rest=${endpoint#*branch=}
    restbranch=$(sanitize "${rest%%&*}")
    ;;
  esac
  ;;
esac

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
    printf 'X-Ratelimit-Resource: %s\r\n' "$resource"
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

# status_reply <status code>: an error answer for the code.
status_reply() {
  local code=$1 text=Error
  case "$code" in
  404) text="Not Found" ;;
  422) text="Unprocessable Entity" ;;
  500) text="Internal Server Error" ;;
  502) text="Bad Gateway" ;;
  504) text="Gateway Timeout" ;;
  esac
  reply "HTTP/2.0 $code $text" "" "{\"message\":\"$text\"}" "gh: $text (HTTP $code)" 1
}

# fail_control <file>: if the control names a status for this call, answer it
# (and count a limited one down).
fail_control() {
  local f="$dir/$1" code count
  [ -f "$f" ] || return 0
  read -r code count <"$f"
  if [ -n "$count" ]; then
    if [ "$count" -le 1 ] 2>/dev/null; then
      rm -f "$f"
    else
      echo "$code $((count - 1))" >"$f"
    fi
  fi
  status_reply "$code"
}

# ---- global failure modes ----------------------------------------------------

failn=$(read_or failn 0)
if [ "$failn" -gt 0 ] 2>/dev/null; then
  echo $((failn - 1)) >"$dir/failn"
  mode=502
fi

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

# ---- per-kind controls ---------------------------------------------------------

fail_control "fail.$kind"
if [ "${#ids[@]}" -gt 1 ]; then
  fail_control "fail.$kind.batch"
fi

# block_wait: hold this call if a block names its kind (see the header).
#
# Claiming a slot (read the count, write it back down) is a critical section:
# two calls of one kind can be in flight together (a refresh and a write both
# reading the pending review), and an unguarded read-then-remove lets both read
# "1" and both be held, so a call the test meant to run through waits for a
# release that only comes after it. mkdir is the atomic lock. A held call is
# recorded in held.K (one line each) once it is actually holding, so a test can
# wait for that instead of for the call to merely have been logged.
block_wait() {
  local f="$dir/block.$kind" lock="$dir/block.$kind.lock" n waited=0 held=no
  [ -f "$f" ] || return 0
  until mkdir "$lock" 2>/dev/null; do sleep 0.01; done
  if [ -f "$f" ]; then
    n=$(tr -d '\n' <"$f")
    case "$n" in '' | *[!0-9]*) n=1 ;; esac
    if [ "$n" -le 1 ]; then rm -f "$f"; else echo $((n - 1)) >"$f"; fi
    held=yes
  fi
  rmdir "$lock"
  [ "$held" = yes ] || return 0
  echo "$$" >>"$dir/held.$kind"
  while [ ! -f "$dir/release.$kind" ] && [ "$waited" -lt 1200 ]; do
    sleep 0.05
    waited=$((waited + 1))
  done
}

# after_hook: apply the kind's state transition once its call is answered.
after_hook() {
  local f="$dir/after.$kind" src dst
  [ -f "$f" ] || return 0
  while read -r src dst; do
    [ -n "$src" ] && [ -n "$dst" ] && cp "$dir/$src" "$dir/$dst"
  done <"$f"
}

rate="\"rateLimit\":{\"cost\":1,\"remaining\":$remaining,\"resetAt\":\"$reset\"}"

# fixture <name...>: the first fixture file that exists, its @REMAINING@ filled.
fixture() {
  local n
  for n in "$@"; do
    if [ -f "$dir/$n" ]; then
      sed "s/@REMAINING@/$remaining/g" "$dir/$n"
      return 0
    fi
  done
  return 1
}

# nodes_reply <file prefix>: the nodes(ids:) answer for the ids of the call.
nodes_reply() {
  local nodes='' id f n
  for id in ${ids[@]+"${ids[@]}"}; do
    f="$dir/$1.$id.json"
    if [ -f "$f" ]; then
      n=$(cat "$f")
    else
      n=null
    fi
    nodes="${nodes:+$nodes,}$n"
  done
  printf '{"data":{%s,"nodes":[%s]}}' "$rate" "$nodes" | sed "s/@REMAINING@/$remaining/g"
}

# gql_reply <fixture names...>: a GraphQL answer from the first fixture present,
# or an unusable-fixture failure naming what was missing.
gql_reply() {
  local body
  if body=$(fixture "$@"); then
    block_wait
    after_hook
    reply "HTTP/2.0 200 OK" "" "$body" "" 0
  fi
  echo "fake-gh: no fixture for $kind (looked for: $*)" >&2
  exit 1
}

case "$kind" in
viewer)
  login=$(read_or viewer.login fixture-viewer)
  reply "HTTP/2.0 200 OK" "" "{\"data\":{\"viewer\":{\"login\":\"$login\"},$rate}}" "" 0
  ;;
search)
  key=$(sanitize "$q")
  file="$dir/search.$key.json"
  [ -f "$file" ] || file="$dir/search.json"
  if [ ! -f "$file" ]; then
    reply "HTTP/2.0 200 OK" "" "{\"data\":{$rate,\"search\":{\"issueCount\":0,\"pageInfo\":{\"hasNextPage\":false,\"endCursor\":\"\"},\"nodes\":[]}}}" "" 0
  fi
  body=$(sed "s/@REMAINING@/$remaining/g" "$file")
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
summary)
  body=$(nodes_reply node)
  block_wait
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
detail)
  body=$(nodes_reply detail)
  block_wait
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
threads | issue_comments | reviews | files | commits | pending)
  gql_reply "$kind.$prkey.json" "$kind.json"
  ;;
thread_comments)
  gql_reply "thread_comments.json"
  ;;
write_items)
  body=$(fixture write_items.json) ||
    body='{"data":{"w0":{"thread":{"id":"PRRT_new1","comments":{"nodes":[{"id":"PRRC_new1"}]}}}}}'
  block_wait
  after_hook
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
update_body)
  body=$(fixture update_body.json) ||
    body='{"data":{"updatePullRequestReview":{"pullRequestReview":{"id":"PRR_new1"}}}}'
  block_wait
  after_hook
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
create_review)
  body=$(fixture create_review.json) || body='{"id":9001,"node_id":"PRR_new1","state":"PENDING"}'
  block_wait
  after_hook
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
runs)
  body=$(fixture "runs.${restrepo}__${restbranch}.json" runs.json) || body='{"total_count":0,"workflow_runs":[]}'
  block_wait
  after_hook
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
jobs)
  body=$(fixture "jobs.${restrun}.json" jobs.json) || body='{"total_count":0,"jobs":[]}'
  block_wait
  after_hook
  reply "HTTP/2.0 200 OK" "" "$body" "" 0
  ;;
rerun)
  body=$(fixture rerun.json) || body='{}'
  block_wait
  after_hook
  reply "HTTP/2.0 201 Created" "" "$body" "" 0
  ;;
*)
  echo "fake-gh: unrecognized request: $endpoint" >&2
  exit 1
  ;;
esac
