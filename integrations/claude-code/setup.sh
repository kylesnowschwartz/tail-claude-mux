#!/usr/bin/env bash
# Install opensessions hooks into Claude Code's ~/.claude/settings.json
# Usage: bash setup.sh
#
# This is idempotent — running it again updates the hooks without duplicating them.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
HOOK_SCRIPT="$SCRIPT_DIR/opensessions-hook.sh"
SETTINGS_FILE="$HOME/.claude/settings.json"

if [ ! -f "$HOOK_SCRIPT" ]; then
  echo "Error: opensessions-hook.sh not found at $HOOK_SCRIPT"
  exit 1
fi

# Ensure ~/.claude exists
mkdir -p "$HOME/.claude"

# Create settings.json if it doesn't exist
if [ ! -f "$SETTINGS_FILE" ]; then
  echo '{}' > "$SETTINGS_FILE"
fi

# Generate the hooks JSON
HOOKS_JSON=$(cat <<EOF
{
  "UserPromptSubmit": [
    {
      "hooks": [
        {
          "type": "command",
          "command": "$HOOK_SCRIPT prompt-submit",
          "timeout": 5
        }
      ]
    }
  ],
  "Stop": [
    {
      "hooks": [
        {
          "type": "command",
          "command": "$HOOK_SCRIPT stop",
          "timeout": 5
        }
      ]
    }
  ],
  "PostToolUse": [
    {
      "hooks": [
        {
          "type": "command",
          "command": "$HOOK_SCRIPT post-tool-use",
          "timeout": 5
        }
      ]
    }
  ],
  "Notification": [
    {
      "hooks": [
        {
          "type": "command",
          "command": "$HOOK_SCRIPT notification",
          "timeout": 5
        }
      ]
    }
  ],
  "SessionStart": [
    {
      "matcher": "startup",
      "hooks": [
        {
          "type": "command",
          "command": "$HOOK_SCRIPT session-start",
          "timeout": 5
        }
      ]
    }
  ],
  "SessionEnd": [
    {
      "hooks": [
        {
          "type": "command",
          "command": "$HOOK_SCRIPT session-end",
          "timeout": 5
        }
      ]
    }
  ]
}
EOF
)

# Merge hooks into existing settings using jq if available, otherwise warn
if command -v jq &>/dev/null; then
  EXISTING=$(cat "$SETTINGS_FILE")
  MERGED=$(echo "$EXISTING" | jq --argjson hooks "$HOOKS_JSON" '.hooks = ($hooks + (.hooks // {}))' 2>/dev/null || echo "$EXISTING" | jq --argjson hooks "$HOOKS_JSON" '. + {"hooks": $hooks}')
  echo "$MERGED" | jq '.' > "$SETTINGS_FILE"
  echo "✅ opensessions hooks installed in $SETTINGS_FILE"
  echo ""
  echo "Events configured:"
  echo "  UserPromptSubmit → running"
  echo "  PostToolUse      → running"
  echo "  Stop             → idle"
  echo "  Notification     → waiting (permission_prompt, elicitation_dialog)"
  echo "  SessionStart     → idle"
  echo "  SessionEnd       → done"
else
  echo "⚠️  jq not found. Please add the following hooks manually to $SETTINGS_FILE:"
  echo ""
  echo "$HOOKS_JSON"
fi
