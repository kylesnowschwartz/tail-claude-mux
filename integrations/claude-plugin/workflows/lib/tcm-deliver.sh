#!/bin/bash
# Committed counterpart of tcm-watch.js's deliveryScript(p). Behavior-frozen:
# see .agent-history or the workflow's git log for the extraction commit.
# Args: SESH PANE SOURCE
#   SESH   - TCM session name to deliver the follow-up to
#   PANE   - pane id, or "" if unknown (server resolves it)
#   SOURCE - absolute path to the source follow-up message file
#
# The first POST's 409 is a refusal. A retry 409 is not: the first POST
# already respawned the pane, so that conflict suggests the resume took;
# only the final receipt grep classifies the retry path.
SESH="$1"
PANE="$2"
SOURCE="$3"
HTTP_CODE=''
RECEIPTS=0
MSGFILE=''
ROLLOUT=''
REASON=''
EVIDENCE=''
post_followup() {
  if [ -n "$PANE" ]; then
    jq -n --arg s "$SESH" --arg p "$PANE" --rawfile m "$SOURCE" '{session:$s, message:$m, pane:$p}' | curl -sS -w '\n%{http_code}' -X POST localhost:7391/followup -H 'Content-Type: application/json' -d @-
  else
    jq -n --arg s "$SESH" --rawfile m "$SOURCE" '{session:$s, message:$m}' | curl -sS -w '\n%{http_code}' -X POST localhost:7391/followup -H 'Content-Type: application/json' -d @-
  fi
}
RESP=$(post_followup 2>&1)
POST_STATUS=$?
if [ "$POST_STATUS" -ne 0 ] || [ -z "$RESP" ]; then
  REASON='transport failure'
  EVIDENCE="transport-failure: ${RESP:-empty response}"
  printf 'RESOLUTION=error HTTP_CODE=%q RECEIPTS=0 MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\n' "$HTTP_CODE" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"
  exit 0
fi
HTTP_CODE=${RESP##*$'\n'}
BODY=${RESP%$'\n'*}
if [ "$HTTP_CODE" = '409' ]; then
  REASON=$(printf '%s' "$BODY" | jq -r '.error // "follow-up refused"' 2>/dev/null)
  EVIDENCE="first-post-409: $REASON"
  printf 'RESOLUTION=refused-409 HTTP_CODE=%q RECEIPTS=0 MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\n' "$HTTP_CODE" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"
  exit 0
fi
if [ "$HTTP_CODE" != '200' ]; then
  EXCERPT=$(printf '%s' "$BODY" | tr '\n' ' ' | cut -c1-240)
  REASON="HTTP $HTTP_CODE"
  EVIDENCE="unexpected-http-$HTTP_CODE: $EXCERPT"
  printf 'RESOLUTION=error HTTP_CODE=%q RECEIPTS=0 MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\n' "$HTTP_CODE" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"
  exit 0
fi
MSGFILE=$(printf '%s' "$BODY" | jq -r '.messageFile // empty')
ROLLOUT=$(printf '%s' "$BODY" | jq -r '.rolloutPath // empty')
sleep 5
RECEIPTS=$(grep -c "$MSGFILE" "$ROLLOUT" 2>/dev/null || true)
RECEIPTS=${RECEIPTS:-0}
if [ "$RECEIPTS" -eq 0 ]; then
  sleep 25
  RECEIPTS=$(grep -c "$MSGFILE" "$ROLLOUT" 2>/dev/null || true)
  RECEIPTS=${RECEIPTS:-0}
fi
if [ "$RECEIPTS" -eq 0 ]; then
  RETRY=$(post_followup 2>&1)
  RETRY_STATUS=$?
  if [ "$RETRY_STATUS" -eq 0 ] && [ -n "$RETRY" ]; then
    HTTP_CODE=${RETRY##*$'\n'}
    RETRY_BODY=${RETRY%$'\n'*}
    if [ "$HTTP_CODE" = '200' ]; then
      MSGFILE=$(printf '%s' "$RETRY_BODY" | jq -r '.messageFile // empty')
      ROLLOUT=$(printf '%s' "$RETRY_BODY" | jq -r '.rolloutPath // empty')
    fi
  else
    HTTP_CODE='transport-error'
  fi
  sleep 30
  RECEIPTS=$(grep -c "$MSGFILE" "$ROLLOUT" 2>/dev/null || true)
  RECEIPTS=${RECEIPTS:-0}
fi
if [ "$RECEIPTS" -gt 0 ]; then
  RESOLUTION='delivered'
  REASON='receipt found'
  EVIDENCE="rollout-receipts=$RECEIPTS"
else
  RESOLUTION='delivery-unverified'
  REASON='no rollout receipt after retry'
  EVIDENCE="final-receipt-grep-zero; retry-http=$HTTP_CODE"
fi
printf 'RESOLUTION=%s HTTP_CODE=%q RECEIPTS=%s MSGFILE=%q ROLLOUT=%q REASON=%q EVIDENCE=%q\n' "$RESOLUTION" "$HTTP_CODE" "$RECEIPTS" "$MSGFILE" "$ROLLOUT" "$REASON" "$EVIDENCE"
