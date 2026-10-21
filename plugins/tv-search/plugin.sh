#!/usr/bin/env bash
set -u

HERDR_SOCK="${HERDR_SOCKET_PATH:-$HOME/.config/herdr/herdr.sock}"
PID=$$
WIDTH=88
HEIGHT=24
CURSOR=0
QUERY=""
CURRENT_CWD="."
CURRENT_WS_LABEL="Dolphin"

C_RESET=$'\033[0m'
C_BOLD=$'\033[1m'
C_DIM=$'\033[2m'
C_ACCENT=$'\033[38;5;63m'
C_PURPLE=$'\033[38;5;135m'
C_GREEN=$'\033[38;5;10m'
C_YELLOW=$'\033[38;5;11m'
C_CYAN=$'\033[38;5;14m'
C_INK=$'\033[38;5;15m'
C_MUTED=$'\033[38;5;7m'
C_SUBTLE=$'\033[38;5;245m'
C_SEL_BG=$'\033[48;5;238m'

send_hello() {
  jq -cn --arg pid "$PID" '{
    type: "hello",
    hello: {
      plugin_id: "bash.tv-search",
      name: "TV Session & File Finder (Bash)",
      version: "0.1.0",
      language: "Bash + jq",
      pid: ($pid | tonumber),
      description: "Case 1 (Modal): Television / Telescope fuzzy finder for Herdr sessions & project files",
      slots: ["modal-surface"],
      rail_items: [
        {
          id: "rail.tv-search",
          plugin_id: "bash.tv-search",
          icon: "",
          label: "TV Finder",
          shortcut: "f2",
          placement: "modal",
          description: "Case 1 (Modal): Search sessions, agents & files (tv / telescope)"
        }
      ],
      commands: [
        {
          id: "tv.open",
          title: "TV Finder (Bash Modal): Search Sessions & Files",
          category: "Rail Plugins",
          shortcut: "f2 / ctrl+f",
          description: "Opens the Television / Telescope modal overlay"
        }
      ]
    }
  }'
}

render_modal() {
  local snap
  snap=$(printf '{"id":"tv","method":"session.snapshot","params":{}}\n' | nc -U "$HERDR_SOCK" 2>/dev/null || true)

  local items_raw=""
  if [[ -n "$snap" ]]; then
    items_raw=$(printf '%s' "$snap" | jq -r '
      (.result.snapshot.workspaces // [])[] |
      "SPACE\t\(.workspace_id)\t#\(.number) \(.label) (\(.agent_status))\t\(.worktree.checkout_path // "/work/lab")\t\(.pane_count) panes · active tab \(.active_tab_id)"
    ' 2>/dev/null || true)
    local agent_items
    agent_items=$(printf '%s' "$snap" | jq -r '
      (.result.snapshot.agents // [])[] |
      "AGENT\t\(.pane_id)\t\(.terminal_title_stripped // .agent) [\(.agent_status)]\t\(.foreground_cwd // .cwd // "")\t\(.tokens.model_effort // .agent) · ctx \(.tokens.context_used_pct // "n/a")"
    ' 2>/dev/null || true)
    if [[ -n "$agent_items" ]]; then
      items_raw="${items_raw}"$'\n'"${agent_items}"
    fi
  fi

  if [[ -d "$CURRENT_CWD" ]]; then
    local file_items
    file_items=$(cd "$CURRENT_CWD" 2>/dev/null && find . -maxdepth 2 -not -path '*/.*' -type f 2>/dev/null | sed 's|^\./||' | head -n 10 | awk -v cwd="$CURRENT_CWD" '{printf "FILE\t%s/%s\t%s\t%s/%s\tProject file in %s\n", cwd, $0, $0, cwd, $0, cwd}')
    if [[ -n "$file_items" ]]; then
      items_raw="${items_raw}"$'\n'"${file_items}"
    fi
  fi

  local -a filtered_kinds=()
  local -a filtered_ids=()
  local -a filtered_labels=()
  local -a filtered_paths=()
  local -a filtered_metas=()

  local q_lower
  q_lower=$(printf '%s' "$QUERY" | tr '[:upper:]' '[:lower:]')

  while IFS=$'\t' read -r kind id label path meta; do
    [[ -z "$kind" ]] && continue
    if [[ -n "$q_lower" ]]; then
      local hay
      hay=$(printf '%s %s %s' "$kind" "$label" "$meta" | tr '[:upper:]' '[:lower:]')
      [[ "$hay" != *"$q_lower"* ]] && continue
    fi
    filtered_kinds+=("$kind")
    filtered_ids+=("$id")
    filtered_labels+=("$label")
    filtered_paths+=("$path")
    filtered_metas+=("$meta")
  done <<< "$items_raw"

  local total=${#filtered_ids[@]}
  if (( total == 0 )); then
    CURSOR=0
  elif (( CURSOR >= total )); then
    CURSOR=$(( total - 1 ))
  elif (( CURSOR < 0 )); then
    CURSOR=0
  fi

  local -a lines=()
  lines+=("${C_BOLD}${C_ACCENT}🔍 TELEVISION (tv) / TELESCOPE MODAL${C_RESET} ${C_SUBTLE}· Bash Plugin (PID $PID) · placement=modal${C_RESET}")
  lines+=("${C_PURPLE}❯${C_RESET} ${C_BOLD}${C_INK}${QUERY}█${C_RESET}  ${C_SUBTLE}[${total} matches · type to filter · ↑/↓ nav · ⏎ jump · Esc close]${C_RESET}")
  lines+=("${C_SUBTLE}$(printf '─%.0s' $(seq 1 $((WIDTH - 4))))${C_RESET}")

  local sel_kind="NONE"
  local sel_id=""
  local sel_label=""
  local sel_path=""
  local sel_meta=""

  if (( total > 0 )); then
    sel_kind="${filtered_kinds[$CURSOR]}"
    sel_id="${filtered_ids[$CURSOR]}"
    sel_label="${filtered_labels[$CURSOR]}"
    sel_path="${filtered_paths[$CURSOR]}"
    sel_meta="${filtered_metas[$CURSOR]}"
  fi

  local list_h=$(( HEIGHT - 8 ))
  (( list_h < 6 )) && list_h=6

  local start=0
  if (( CURSOR >= list_h )); then
    start=$(( CURSOR - list_h + 1 ))
  fi

  for (( i = 0; i < list_h; i++ )); do
    local idx=$(( start + i ))
    if (( idx < total )); then
      local k="${filtered_kinds[$idx]}"
      local lbl="${filtered_labels[$idx]}"
      local mt="${filtered_metas[$idx]}"
      local badge_col="$C_CYAN"
      [[ "$k" == "SPACE" ]] && badge_col="$C_GREEN"
      [[ "$k" == "AGENT" ]] && badge_col="$C_YELLOW"
      if (( idx == CURSOR )); then
        lines+=("${C_SEL_BG}${C_BOLD}${C_ACCENT}▎${badge_col}[${k}]${C_INK} ${lbl:0:32} ${C_SUBTLE}— ${mt:0:34}${C_RESET}")
      else
        lines+=("  ${badge_col}[${k}]${C_MUTED} ${lbl:0:32} ${C_SUBTLE}— ${mt:0:34}${C_RESET}")
      fi
    else
      lines+=("")
    fi
  done

  lines+=("${C_SUBTLE}$(printf '─%.0s' $(seq 1 $((WIDTH - 4))))${C_RESET}")
  lines+=("${C_BOLD}${C_PURPLE}PREVIEW:${C_RESET} ${C_INK}${sel_label}${C_RESET} ${C_SUBTLE}(${sel_id})${C_RESET}")
  if [[ "$sel_kind" == "FILE" && -f "$sel_path" ]]; then
    local preview_line
    preview_line=$(head -n 2 "$sel_path" 2>/dev/null | tr '\n\t' '  ' | cut -c1-76)
    lines+=("  ${C_MUTED}${preview_line}${C_RESET}")
  else
    lines+=("  ${C_MUTED}Path: ${sel_path}  ·  ${sel_meta}${C_RESET}")
  fi
  lines+=("  ${C_SUBTLE}Press Enter to focus in Herdr & close modal  │  Esc to dismiss${C_RESET}")

  local json_lines
  json_lines=$(printf '%s\n' "${lines[@]}" | jq -R . | jq -s .)

  jq -cn \
    --arg title "🔍 TV Session & File Finder (Case 1: Modal)" \
    --arg badge "MODAL · BASH" \
    --argjson lines "$json_lines" \
    '{
      type: "slot_update",
      slot_update: {
        plugin_id: "bash.tv-search",
        slot: "modal-surface",
        title: $title,
        badge: $badge,
        lines: $lines
      }
    }'
}

activate_selection() {
  local snap
  snap=$(printf '{"id":"tv","method":"session.snapshot","params":{}}\n' | nc -U "$HERDR_SOCK" 2>/dev/null || true)
  local items_raw=""
  if [[ -n "$snap" ]]; then
    items_raw=$(printf '%s' "$snap" | jq -r '
      (.result.snapshot.workspaces // [])[] |
      "SPACE\t\(.workspace_id)\t#\(.number) \(.label)"
    ' 2>/dev/null || true)
    local agent_items
    agent_items=$(printf '%s' "$snap" | jq -r '
      (.result.snapshot.agents // [])[] |
      "AGENT\t\(.pane_id)\t\(.terminal_title_stripped // .agent)"
    ' 2>/dev/null || true)
    [[ -n "$agent_items" ]] && items_raw="${items_raw}"$'\n'"${agent_items}"
  fi
  if [[ -d "$CURRENT_CWD" ]]; then
    local file_items
    file_items=$(cd "$CURRENT_CWD" 2>/dev/null && find . -maxdepth 2 -not -path '*/.*' -type f 2>/dev/null | sed 's|^\./||' | head -n 10 | awk -v cwd="$CURRENT_CWD" '{printf "FILE\t%s/%s\t%s\n", cwd, $0, $0}')
    [[ -n "$file_items" ]] && items_raw="${items_raw}"$'\n'"${file_items}"
  fi

  local q_lower
  q_lower=$(printf '%s' "$QUERY" | tr '[:upper:]' '[:lower:]')
  local idx=0
  while IFS=$'\t' read -r kind id label; do
    [[ -z "$kind" ]] && continue
    if [[ -n "$q_lower" ]]; then
      local hay
      hay=$(printf '%s %s' "$kind" "$label" | tr '[:upper:]' '[:lower:]')
      [[ "$hay" != *"$q_lower"* ]] && continue
    fi
    if (( idx == CURSOR )); then
      if [[ "$kind" == "SPACE" ]]; then
        printf '{"id":"tv-f","method":"workspace.focus","params":{"workspace_id":"%s"}}\n' "$id" | nc -U "$HERDR_SOCK" >/dev/null 2>&1 || true
      elif [[ "$kind" == "AGENT" ]]; then
        printf '{"id":"tv-a","method":"agent.focus","params":{"pane_id":"%s"}}\n' "$id" | nc -U "$HERDR_SOCK" >/dev/null 2>&1 || true
      fi
      jq -cn --arg lbl "$label" --arg k "$kind" '{
        type: "toast",
        close_modal: true,
        toast: {
          plugin_id: "bash.tv-search",
          level: "success",
          title: "TV Finder Selected",
          message: ("Jumped to " + $k + ": " + $lbl)
        }
      }'
      return
    fi
    idx=$(( idx + 1 ))
  done <<< "$items_raw"
}

send_hello

while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  msg_type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null || true)
  case "$msg_type" in
    welcome)
      CURRENT_CWD=$(printf '%s' "$line" | jq -r '.welcome.context.cwd // "."')
      ;;
    workspace_context)
      CURRENT_CWD=$(printf '%s' "$line" | jq -r '.workspace_context.cwd // "."')
      CURRENT_WS_LABEL=$(printf '%s' "$line" | jq -r '.workspace_context.workspace_label // "Dolphin"')
      ;;
    rail_activated)
      WIDTH=$(printf '%s' "$line" | jq -r '.rail_activated.width // 88')
      HEIGHT=$(printf '%s' "$line" | jq -r '.rail_activated.height // 24')
      CURRENT_CWD=$(printf '%s' "$line" | jq -r '.rail_activated.context.cwd // "."')
      QUERY=""
      CURSOR=0
      render_modal
      ;;
    slot_key)
      key=$(printf '%s' "$line" | jq -r '.slot_key.key // empty')
      case "$key" in
        up|ctrl+k)
          CURSOR=$(( CURSOR - 1 ))
          render_modal
          ;;
        down|ctrl+j|tab)
          CURSOR=$(( CURSOR + 1 ))
          render_modal
          ;;
        backspace)
          if [[ -n "$QUERY" ]]; then
            QUERY="${QUERY%?}"
            CURSOR=0
          fi
          render_modal
          ;;
        enter)
          activate_selection
          ;;
        *)
          if [[ ${#key} -eq 1 ]]; then
            QUERY="${QUERY}${key}"
            CURSOR=0
            render_modal
          fi
          ;;
      esac
      ;;
  esac
done
