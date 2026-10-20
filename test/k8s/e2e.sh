#!/usr/bin/env bash
# End-to-end test of the GameCluster operator on a real Kubernetes cluster.
#
# Prerequisites: the chart is installed (see test/k8s/README.md), e.g.
#   helm install gs deploy/helm/gameserver -n game --create-namespace
#
# Scenario (about 8 minutes), with robots playing the whole time:
#   1. Scale out:   160 robots at 50 players/node -> the operator grows the game tier to 4 nodes
#   2. Node crash:  one game pod is force-deleted -> replaced; its players reconnect elsewhere
#   3. Rolling update with players online -> every game pod replaced, one at a time
#   4. Robots report: nobody failed to log in or stayed disconnected
#   5. Scale in:    robots leave -> the tier shrinks back to minReplicas by draining
#   6. Data:        every robot's character is in MongoDB
#
# Usage: test/k8s/e2e.sh        (env: NS=game GC=demo ROBOTS=160)
set -euo pipefail

NS=${NS:-game}
GC=${GC:-demo}
ROBOTS=${ROBOTS:-160}
TARGET=50 # players per node, set below
OUT=${OUT:-test/k8s/out}
mkdir -p "$OUT"

pass=0
ok()   { echo "  PASS  $*"; pass=$((pass + 1)); }
fail() { echo "  FAIL  $*"; dump; exit 1; }
step() { echo; echo "== $* =="; }
gc()   { kubectl -n "$NS" get gc "$GC" -o jsonpath="{$1}"; }
dump() {
  kubectl -n "$NS" get gc "$GC" -o wide || true
  kubectl -n "$NS" get pods -o wide || true
  kubectl -n "$NS" get events --field-selector involvedObject.kind=GameCluster --sort-by=.lastTimestamp | tail -30 || true
}
# wait_for <description> <timeout seconds> <shell condition>
wait_for() {
  local what=$1 timeout=$2 cond=$3 start=$SECONDS
  until eval "$cond"; do
    if ((SECONDS - start > timeout)); then fail "timed out after ${timeout}s: $what"; fi
    sleep 3
  done
  ok "$what ($((SECONDS - start))s)"
}
nodes_line() { gc '.status.game.ready'; printf ' ready / '; gc '.status.game.desired'; printf ' desired, players='; gc '.status.players'; echo; }

step "0. Baseline"
kubectl -n "$NS" patch gc "$GC" --type merge -p "{\"spec\":{\"game\":{\"drainTimeout\":\"20s\",\"autoscale\":{\"minReplicas\":2,\"maxReplicas\":6,\"targetPlayersPerNode\":$TARGET,\"scaleDownDelay\":\"30s\"}}}}" >/dev/null
wait_for "cluster Running with 2 game nodes" 180 '[ "$(gc .status.phase)" = Running ] && [ "$(gc .status.game.ready)" = 2 ]'
first_nodes=$(gc '.status.nodes[*].name')
echo "  game nodes: $first_nodes"

step "1. Scale out under load"
kubectl -n "$NS" delete job robots --ignore-not-found >/dev/null
kubectl -n "$NS" apply -f - >/dev/null <<EOF
apiVersion: batch/v1
kind: Job
metadata: { name: robots }
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: robot
          image: $(gc .spec.image)
          imagePullPolicy: IfNotPresent
          command: [/app/robot]
          args: [-addr, "$GC-gate:7000", -n, "$ROBOTS", -duration, 6m, -ramp, 20ms, -move-interval, 1s, -reconnect, -prefix, k8s]
EOF
want=$(( (ROBOTS + TARGET - 1) / TARGET ))
wait_for "$ROBOTS players online" 120 '[ "$(gc .status.players)" -ge "$ROBOTS" ]'
wait_for "autoscaled to $want ready game nodes" 120 '[ "$(gc .status.game.desired)" = "$want" ] && [ "$(gc .status.game.ready)" = "$want" ]'
nodes_line

step "2. Node crash"
# The busiest node: the crash must actually hurt players.
busiest=$(kubectl -n "$NS" get gc "$GC" -o jsonpath='{range .status.nodes[*]}{.players} {.name}{"\n"}{end}' | sort -rn | head -1)
victim=${busiest#* }
victim_players=${busiest%% *}
echo "  force-deleting $victim with $victim_players players (no graceful stop, like a kernel panic)"
[ "$victim_players" -gt 0 ] || fail "busiest node has no players"
kubectl -n "$NS" delete pod "$victim" --grace-period=0 --force >/dev/null 2>&1
wait_for "crashed node replaced" 120 '! gc ".status.nodes[*].name" | grep -qw "$victim" && [ "$(gc .status.game.ready)" = "$want" ]'
echo "  players right after the crash: $(gc .status.players)"
wait_for "its players reconnected ($ROBOTS online)" 120 '[ "$(gc .status.players)" -ge "$ROBOTS" ]'

step "3. Rolling update with players online"
old_version=$(gc '.status.nodes[0].version')
new_image=$(gc .spec.image | sed 's/:.*//'):e2e-v2
echo "  new image $new_image (must already be loaded into the cluster)"
kubectl -n "$NS" patch gc "$GC" --type merge -p "{\"spec\":{\"image\":\"$new_image\"}}" >/dev/null
wait_for "phase Updating" 30 '[ "$(gc .status.phase)" = Updating ]'
wait_for "all game nodes replaced" 300 '[ "$(gc .status.game.outdated)" = 0 ] && [ "$(gc .status.game.draining)" = 0 ] && [ "$(gc .status.game.ready)" = "$want" ] && ! gc ".status.nodes[*].version" | grep -qw "$old_version"'
wait_for "players back on the new nodes" 120 '[ "$(gc .status.players)" -ge "$ROBOTS" ]'
nodes_line

step "4. Robot report"
wait_for "robots finished" 420 '[ -n "$(kubectl -n "$NS" get job robots -o jsonpath="{.status.completionTime}{.status.failed}")" ]'
kubectl -n "$NS" logs job/robots | sed -n '/robot report/,$p' | tee "$OUT/robot-report.txt"
[ "$(kubectl -n "$NS" get job robots -o jsonpath='{.status.succeeded}')" = 1 ] || fail "robot job failed (login failures or robots that never got back in)"
ok "every robot logged in and every disconnected robot got back in"

step "5. Scale in after players leave"
wait_for "players gone" 60 '[ "$(gc .status.players)" = 0 ]'
wait_for "drained back to 2 game nodes" 240 '[ "$(gc .status.game.ready)" = 2 ] && [ "$(gc .status.game.draining)" = 0 ] && [ "$(gc .status.phase)" = Running ]'

step "6. Data"
count=$(kubectl -n "$NS" exec deploy/gs-mongo -- mongosh --quiet game --eval 'db.players.countDocuments({name: /^k8s_/})' | tr -d '\r')
[ "$count" -ge "$ROBOTS" ] || fail "only $count robot characters in MongoDB"
ok "$count robot characters saved in MongoDB"

kubectl -n "$NS" get events --field-selector involvedObject.kind=GameCluster --sort-by=.lastTimestamp > "$OUT/events.txt" || true
kubectl -n "$NS" get gc "$GC" -o yaml > "$OUT/gamecluster.yaml"
echo
echo "ALL $pass CHECKS PASSED"
