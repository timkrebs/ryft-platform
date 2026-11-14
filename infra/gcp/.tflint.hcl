# tflint --init && tflint
# Only the bundled terraform plugin: no ruleset download, so no GitHub API call (its rate limit makes a
# downloaded ruleset flaky in CI).
plugin "terraform" {
  enabled = true
  preset  = "recommended"
}
