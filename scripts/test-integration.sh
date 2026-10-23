#!/usr/bin/env bash
# Only creates disposable containers; never reads .env or mounts existing data.
set -euo pipefail
cd "$(dirname "$0")/../cloudnest_go"
run_id="cloudnest-test-$(date +%s)-$$"
mysql_id=""
redis_id=""
cleanup() {
  if [[ -n "$mysql_id" ]]; then docker rm -fv "$mysql_id" >/dev/null; fi
  if [[ -n "$redis_id" ]]; then docker rm -fv "$redis_id" >/dev/null; fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
mysql_id=$(docker run -d --name "$run_id-mysql" --label cloudnest.test=true \
  -p 127.0.0.1::3306 \
  -e MYSQL_ROOT_PASSWORD=cloudnest-disposable-test \
  -e MYSQL_DATABASE=cloudnest_test -e MYSQL_USER=cloudnest_test \
  -e MYSQL_PASSWORD=cloudnest-disposable-test mysql:8.0 --log-bin-trust-function-creators=1)
redis_id=$(docker run -d --name "$run_id-redis" --label cloudnest.test=true \
  -p 127.0.0.1::6379 redis:7.4-alpine)
ready=false
for ((attempt=0; attempt<90; attempt++)); do
  if docker exec -e MYSQL_PWD=cloudnest-disposable-test "$mysql_id" \
    mysql -h 127.0.0.1 -u cloudnest_test cloudnest_test -e 'SELECT 1' >/dev/null 2>&1 \
    && docker exec "$redis_id" redis-cli ping >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
if [[ "$ready" != true ]]; then
  echo 'Disposable MySQL/Redis did not become ready within 90 seconds.' >&2
  exit 1
fi
# Metadata visibility is required only for GC maintenance tests. These grants
# and trigger settings apply solely to this newly created disposable container.
docker exec -e MYSQL_PWD=cloudnest-disposable-test "$mysql_id" mysql -u root \
  -e "GRANT PROCESS ON *.* TO 'cloudnest_test'@'%'; CREATE USER 'cloudnest_gc_limited'@'%' IDENTIFIED BY 'cloudnest-disposable-test'; GRANT SELECT ON cloudnest_test.* TO 'cloudnest_gc_limited'@'%';" >/dev/null
mysql_addr=$(docker port "$mysql_id" 3306/tcp)
redis_addr=$(docker port "$redis_id" 6379/tcp)
export CLOUDNEST_TEST_DISPOSABLE=1
export CLOUDNEST_TEST_MYSQL_DSN="cloudnest_test:cloudnest-disposable-test@tcp($mysql_addr)/cloudnest_test?charset=utf8mb4&parseTime=True&loc=UTC"
export CLOUDNEST_TEST_REDIS_ADDR="$redis_addr"
go test -tags=integration -race -count=1 -timeout=6m -v ./integration "$@"
