# bats file_tags=type:unit

SCRIPT="$(cd "$(dirname "$BATS_TEST_FILENAME")/.." && pwd)/chunk-for-bd-field.sh"

setup() {
  TEST_DIR="$(mktemp -d)"
}

teardown() {
  rm -rf "$TEST_DIR"
}

# Concatenates chunk files listed (one per line) on stdin, in order, with no
# separator, to a target path.
concat_chunks() {
  local out="$1"
  : >"$out"
  while IFS= read -r chunk; do
    cat "$chunk" >>"$out"
  done
  echo "$out"
}

@test "input under the cap produces exactly one chunk with identical content" {
  printf 'line one\nline two\nline three\n' >"$TEST_DIR/in.txt"
  run "$SCRIPT" "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ "${lines[0]}" = "$TEST_DIR/out.1" ]
  diff "$TEST_DIR/in.txt" "$TEST_DIR/out.1"
}

@test "input over the cap splits into multiple chunks, none exceeding max-bytes" {
  for i in $(seq 1 500); do
    printf 'this is line number %03d of the test fixture content\n' "$i"
  done >"$TEST_DIR/in.txt"
  run "$SCRIPT" --max-bytes 500 "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -gt 1 ]
  for chunk in "${lines[@]}"; do
    size=$(wc -c <"$chunk")
    [ "$size" -le 500 ]
  done
}

@test "concatenated chunks reproduce the original file byte-for-byte" {
  for i in $(seq 1 500); do
    printf 'this is line number %03d of the test fixture content\n' "$i"
  done >"$TEST_DIR/in.txt"
  run "$SCRIPT" --max-bytes 500 "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  printf '%s\n' "${lines[@]}" | concat_chunks "$TEST_DIR/reassembled.txt"
  diff "$TEST_DIR/in.txt" "$TEST_DIR/reassembled.txt"
}

@test "empty input produces exactly one empty chunk" {
  : >"$TEST_DIR/in.txt"
  run "$SCRIPT" "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  [ "${#lines[@]}" -eq 1 ]
  [ ! -s "${lines[0]}" ]
}

@test "a single line longer than max-bytes is a hard error, not a silent oversized chunk" {
  printf '%s\n' "$(head -c 200 </dev/zero | tr '\0' 'x')" >"$TEST_DIR/in.txt"
  run "$SCRIPT" --max-bytes 100 "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -ne 0 ]
  [[ "$output" == *"exceeds --max-bytes"* ]]
}

@test "missing input file is a clear error, not a crash" {
  run "$SCRIPT" "$TEST_DIR/does-not-exist.txt" "$TEST_DIR/out"
  [ "$status" -ne 0 ]
  [[ "$output" == *"no such file"* ]]
}

@test "a file with no trailing newline on its last line round-trips exactly" {
  printf 'first line\nsecond line\nlast line with no trailing newline' >"$TEST_DIR/in.txt"
  run "$SCRIPT" --max-bytes 40 "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  printf '%s\n' "${lines[@]}" | concat_chunks "$TEST_DIR/reassembled.txt"
  diff "$TEST_DIR/in.txt" "$TEST_DIR/reassembled.txt"
  last_chunk="${lines[-1]}"
  last_byte="$(tail -c1 "$last_chunk" | od -An -tx1 | tr -d ' ')"
  [ "$last_byte" != "0a" ]
}

@test "multi-byte UTF-8 content round-trips exactly (byte-safe chunking)" {
  printf 'héllo wörld — emoji: 🎉🎉🎉\nsecond liné with more accénts\n' >"$TEST_DIR/in.txt"
  run "$SCRIPT" --max-bytes 40 "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -eq 0 ]
  for chunk in "${lines[@]}"; do
    size=$(wc -c <"$chunk")
    [ "$size" -le 40 ]
  done
  printf '%s\n' "${lines[@]}" | concat_chunks "$TEST_DIR/reassembled.txt"
  diff "$TEST_DIR/in.txt" "$TEST_DIR/reassembled.txt"
}

@test "--help exits 0 and prints usage" {
  run "$SCRIPT" --help
  [ "$status" -eq 0 ]
  [[ "$output" == *"Usage: chunk-for-bd-field.sh"* ]]
}

@test "a non-numeric --max-bytes is a usage error" {
  run "$SCRIPT" --max-bytes abc "$TEST_DIR/in.txt" "$TEST_DIR/out"
  [ "$status" -eq 2 ]
  [[ "$output" == *"must be a positive integer"* ]]
}

@test "wrong argument count is a usage error" {
  run "$SCRIPT" "$TEST_DIR/in.txt"
  [ "$status" -eq 2 ]
  [[ "$output" == *"expected <input-file> <output-prefix>"* ]]
}
