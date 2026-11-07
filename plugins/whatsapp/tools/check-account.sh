#!/usr/bin/env bash
# Read-only check of a WhatsApp Cloud API setup. It asks Meta about your account,
# phone number and templates and prints what comes back. It changes nothing.
#
# The token is typed without being shown and is never written to a file or
# printed. The output is safe to share.
#
#   bash check-account.sh
set -uo pipefail

API="${META_API_BASE:-https://graph.facebook.com}"
VER="${META_GRAPH_VERSION:-v23.0}"

read -r -p "Phone number ID: " PHONE
read -r -p "Messaging account (WhatsApp Business Account) ID: " WABA
read -r -s -p "Access token (hidden): " TOKEN
echo; echo

# Asks for one field at a time, so one field Meta does not offer cannot hide the rest.
field() { # $1 = object id, $2 = field
  curl -sS -m 20 -H "Authorization: Bearer $TOKEN" "$API/$VER/$1?fields=$2" 2>&1 | python3 -c '
import sys, json
field = sys.argv[1]; token = sys.argv[2]
raw = sys.stdin.read().replace(token, "***")
try:
    d = json.loads(raw)
except Exception:
    print(f"  {field:<32} (no answer: {raw.strip()[:100]})"); sys.exit()
if "error" in d:
    e = d["error"]; print(f"  {field:<32} (not available: {e.get('"'"'message'"'"','"'"''"'"')[:90]} [code {e.get('"'"'code'"'"')}])")
else:
    print(f"  {field:<32} {d.get(field, '"'"'(empty)'"'"')}")
' "$2" "$TOKEN"
}

echo "WhatsApp Business Account  ($WABA)"
for f in name account_review_status business_verification_status ownership_type currency; do field "$WABA" "$f"; done
echo
echo "Phone number  ($PHONE)"
for f in display_phone_number verified_name name_status status code_verification_status quality_rating platform_type account_mode messaging_limit_tier; do field "$PHONE" "$f"; done
echo
echo "Message templates on the account"
curl -sS -m 20 -H "Authorization: Bearer $TOKEN" "$API/$VER/$WABA/message_templates?fields=name,status,language,category&limit=25" 2>&1 | python3 -c '
import sys, json
token = sys.argv[1]
raw = sys.stdin.read().replace(token, "***")
try:
    d = json.loads(raw)
except Exception:
    print("  (no answer)"); sys.exit()
if "error" in d:
    e = d["error"]; print(f"  (not available: {e.get('"'"'message'"'"','"'"''"'"')[:100]} [code {e.get('"'"'code'"'"')}])"); sys.exit()
rows = d.get("data", [])
print("  none yet" if not rows else "")
for t in rows:
    print(f"  {t.get('"'"'name'"'"',''):<34} {t.get('"'"'status'"'"',''):<10} {t.get('"'"'language'"'"',''):<7} {t.get('"'"'category'"'"','"'"''"'"')}")
' "$TOKEN"
echo
echo "Done. Nothing was changed. Share this output, not your token."
