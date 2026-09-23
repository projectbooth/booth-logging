#!/bin/sh
# Prints the environment the Go tests need to reach hack/docker-compose.loki.yml's Loki.
#   eval "$(sh hack/test-env.sh)"
echo "export BOOTH_TEST_LOKI_URL=http://localhost:3100"
