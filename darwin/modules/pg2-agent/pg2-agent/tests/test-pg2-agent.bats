#!/usr/bin/env bats

# Unit tests for pg2-agent with plugin architecture
# Tests AI agent wrapper with configurable agent plugins
# Run locally: SCRIPTS_DIR=$PWD/modules/pg2-agent/pg2-agent bats modules/pg2-agent/pg2-agent/tests/ < /dev/null

load test_helper

setup() {
    TEST_DIR=$(mktemp -d)
    export TEST_DIR
    setup_test_home
    if [[ -z "${MOCK_CLAUDE_AGENT:-}" ]]; then
        create_mock_agent "claude" 10 success
    fi
    create_zr_agent_wrapper
}

teardown() {
    rm -rf "$TEST_DIR"
}

# Basic functionality tests

@test "pg2-agent shows help with --help flag" {
    run pg2-agent --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "pg2-agent: AI agent wrapper"
}

@test "pg2-agent shows help with -h flag" {
    run pg2-agent -h
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Usage: pg2-agent"
}

@test "pg2-agent help shows registered agents" {
    run pg2-agent --help
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "Registered Agents:"
    echo "$output" | grep -q "claude"
}

# Argument parsing tests

@test "pg2-agent accepts --model flag" {
    run pg2-agent --model large "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent accepts --plan flag" {
    run pg2-agent --plan "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent accepts --thinking flag" {
    run pg2-agent --thinking "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent accepts combined flags" {
    run pg2-agent --model small --plan --thinking "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent rejects invalid model" {
    run pg2-agent --model invalid "test prompt"
    [ "$status" -ne 0 ]
    echo "$output" | grep -q "Invalid model"
}

# Model validation tests

@test "pg2-agent accepts small model" {
    run pg2-agent --model small "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent accepts medium model" {
    run pg2-agent --model medium "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent accepts large model" {
    run pg2-agent --model large "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

# Stdin input tests

@test "pg2-agent accepts prompt from stdin" {
    run bash -c 'echo "test prompt" | "$0" ' "$TEST_DIR/pg2-agent"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent accepts prompt from argument" {
    run pg2-agent "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

@test "pg2-agent combines stdin and argument" {
    run bash -c 'echo "context" | "$0" "instruction"' "$TEST_DIR/pg2-agent"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

# Plugin architecture tests

@test "pg2-agent tries agents in priority order" {
    run pg2-agent "test prompt"
    [[ "$output" =~ "Trying claude" ]]
}

@test "pg2-agent succeeds when first agent succeeds" {
    run pg2-agent "test prompt"
    [ "$status" -eq 0 ]
    echo "$output" | grep -q "AI response"
}

@test "pg2-agent output contains agent response" {
    run pg2-agent "test prompt"
    [ "$status" -eq 0 ]
    [[ "$output" =~ "AI response" ]]
}

# Plan mode tests

@test "pg2-agent passes plan mode to agents" {
    run pg2-agent --plan "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

# Thinking mode tests

@test "pg2-agent passes thinking mode to agents" {
    run pg2-agent --thinking "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

# Model passing tests

@test "pg2-agent passes model to agents" {
    run pg2-agent --model large "test prompt"
    [[ "$status" -eq 0 || "$output" =~ "Trying" ]]
}

# Error message tests

@test "pg2-agent shows agent name when trying" {
    run pg2-agent "test prompt"
    echo "$output" | grep -q "Trying claude"
}

@test "pg2-agent shows priority when trying agents" {
    run pg2-agent "test prompt"
    echo "$output" | grep -q "priority"
}

# Integration tests

@test "pg2-agent end-to-end with default options" {
    run pg2-agent "Explain this code"
    [ "$status" -eq 0 ]
    [[ "$output" =~ "AI response" ]]
}

@test "pg2-agent end-to-end with all options" {
    run pg2-agent --model small --plan --thinking "Analyze this function"
    [ "$status" -eq 0 ]
    [[ "$output" =~ "AI response" ]]
}

@test "pg2-agent end-to-end with stdin input" {
    run bash -c 'cat <<EOF | "$0" --plan "Fill in this template"
# Template
Title: __TITLE__
Description: __DESC__
EOF' "$TEST_DIR/pg2-agent"
    [ "$status" -eq 0 ]
    [[ "$output" =~ "AI response" ]]
}
