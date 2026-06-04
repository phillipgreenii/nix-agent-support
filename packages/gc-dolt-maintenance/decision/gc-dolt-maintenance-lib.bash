# shellcheck shell=bash
# Pure decision: prints "yes:<reason>" or "no:<reason>". No side effects.
should_flatten() {
  local commit=$1 busy=$2 hours=$3 breaker=$4 remote=$5
  local commit_thr=$6 busy_thr=$7 min_h=$8 max_h=$9
  [[ $remote == 1 ]] && {
    echo "no:remote-connected db"
    return 0
  }
  [[ $breaker != 1 ]] && {
    echo "no:breaker not applied"
    return 0
  }
  ((commit < commit_thr)) && {
    echo "no:below commit threshold"
    return 0
  }
  ((hours >= max_h)) && {
    echo "yes:max-age force"
    return 0
  }
  ((hours < min_h)) && {
    echo "no:flattened too recently"
    return 0
  }
  ((busy >= busy_thr)) && {
    echo "no:system busy"
    return 0
  }
  echo "yes:need met, safe, interval ok"
}
