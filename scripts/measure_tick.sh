#!/usr/bin/env bash
# Measure p99 CPU and RSS of andara-server under the sizing fixture and record the result
# in deploy/helm/andara/measurements.yaml (AW-INF-003 AC-10).
#
# Refuses to record anything until the server exposes andara_tick_duration_seconds: a
# measurement of a process with no tick loop would be a number with nothing behind it,
# and the chart would size production on it. Until AW-SRV-002 lands, this exits 2 and
# measurements.yaml keeps `measured: false`, which `make helm-test` warns about.
set -euo pipefail

DURATION="${1:-300}"
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO"

OUT="deploy/helm/andara/measurements.yaml"
FIXTURE="${ANDARA_MEASURE_FIXTURE:-testdata/content/valid}"
PORT="${ANDARA_MEASURE_PORT:-18080}"

[[ -x bin/andara-server ]] || make build >/dev/null

ANDARA_CONTENT_SOURCE=dir ANDARA_CONTENT_PATH="$FIXTURE" ANDARA_HTTP_PORT="$PORT" \
ANDARA_LOG_LEVEL=warn ANDARA_LOG_FORMAT=json ANDARA_OTLP_ENDPOINT="" \
  bin/andara-server >/dev/null 2>&1 &
PID=$!
trap 'kill "$PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 30); do
  curl -sf "http://127.0.0.1:${PORT}/readyz" >/dev/null 2>&1 && break
  sleep 1
done
curl -sf "http://127.0.0.1:${PORT}/readyz" >/dev/null || {
  echo "make: measure-tick: server did not become ready on :$PORT" >&2; exit 1
}

if ! curl -sf "http://127.0.0.1:${PORT}/metrics" | grep -q '^andara_tick_duration_seconds'; then
  echo "make: measure-tick: no andara_tick_duration_seconds on /metrics — there is no tick loop" >&2
  echo "make: measure-tick: refusing to record a measurement until AW-SRV-002 lands (measured: false stays)" >&2
  exit 2
fi

echo "measure-tick: sampling pid $PID for ${DURATION}s against $FIXTURE"
SAMPLES="$(mktemp)"
for _ in $(seq 1 "$DURATION"); do
  # %cpu is the lifetime average from ps; sample the cumulative jiffies instead and
  # difference them so each sample is the last second's CPU, not the average since start.
  read -r utime stime rss < <(awk '{print $14, $15, $24}' "/proc/$PID/stat")
  echo "$(( (utime + stime) * 1000 / $(getconf CLK_TCK) )) $(( rss * $(getconf PAGESIZE) / 1048576 ))" >>"$SAMPLES"
  sleep 1
done

python3 - "$SAMPLES" "$OUT" "$DURATION" "$FIXTURE" <<'PY'
import sys, datetime, yaml
samples, out, duration, fixture = sys.argv[1:]
rows = [tuple(map(int, l.split())) for l in open(samples) if l.strip()]
cpu = [b[0] - a[0] for a, b in zip(rows, rows[1:])]      # ms of CPU per second = millicores
rss = [r[1] for r in rows]
def p99(xs):
    xs = sorted(xs); return xs[min(len(xs) - 1, int(len(xs) * 0.99))]
doc = yaml.safe_load(open(out))
doc["measured"] = True
doc["measured_at"] = datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
doc["fixture"] = fixture
doc["server"] = {"cpu_millicores_p99": max(1, p99(cpu)), "memory_mib_p99": max(1, p99(rss))}
with open(out, "w") as f:
    f.write("# Measured tick cost, committed from `make measure-tick` (AW-INF-003 AC-10).\n"
            "# Integers: millicores and MiB, so Helm can multiply them. Re-run after any\n"
            "# change to the tick loop or the sizing fixture; helm-test warns when prod\n"
            "# requests drift past 2x these numbers.\n")
    yaml.safe_dump(doc, f, sort_keys=False)
print("measure-tick: p99 cpu=%dm rss=%dMi over %ss -> %s" % (doc["server"]["cpu_millicores_p99"], doc["server"]["memory_mib_p99"], duration, out))
PY
