#!/usr/bin/env bash
set -u

HERDR_SOCK="${HERDR_SOCKET_PATH:-$HOME/.config/herdr/herdr.sock}"
PID=$$
WIDTH=128
HEIGHT=34
CURSOR=0

C_RESET=$'\033[0m'
C_BOLD=$'\033[1m'
C_ACCENT=$'\033[38;2;122;162;247m'
C_PURPLE=$'\033[38;2;187;154;247m'
C_GREEN=$'\033[38;2;158;206;106m'
C_YELLOW=$'\033[38;2;224;175;104m'
C_CYAN=$'\033[38;2;125;207;255m'
C_INK=$'\033[38;2;192;202;245m'
C_MUTED=$'\033[38;2;169;177;214m'
C_SUBTLE=$'\033[38;2;86;95;137m'
C_SEL_BG=$'\033[48;2;40;52;87m'

send_hello() {
  jq -cn --arg pid "$PID" '{
    type: "hello",
    hello: {
      plugin_id: "bash.fleet-dashboard",
      name: "Cross-Project Fleet & Plugin Hub (Bash)",
      version: "0.1.0",
      language: "Bash + jq",
      pid: ($pid | tonumber),
      description: "Case 2 (Full-Canvas): Cross-project dashboard & plugin manager occupying the entire canvas",
      slots: ["full-canvas"],
      rail_items: [
        {
          id: "rail.fleet-dashboard",
          plugin_id: "bash.fleet-dashboard",
          icon: "",
          label: "Fleet Hub",
          shortcut: "f3",
          placement: "full-canvas",
          description: "Case 2 (Full-Canvas): Cross-project fleet overview & plugin manager"
        }
      ],
      commands: [
        {
          id: "fleet.open",
          title: "Fleet Hub (Bash Full-Canvas): Open Cross-Project Dashboard & Plugin Manager",
          category: "Rail Plugins",
          shortcut: "f3 / ctrl+g",
          description: "Displays across the entire canvas (hiding the workspace sidebar)"
        }
      ]
    }
  }'
}

render_full_canvas() {
  local snap
  snap=$(printf '{"id":"fleet","method":"session.snapshot","params":{}}\n' | nc -U "$HERDR_SOCK" 2>/dev/null || true)

  local ws_count=0
  local tab_count=0
  local pane_count=0
  local agent_count=0
  local working_count=0
  if [[ -n "$snap" ]]; then
    read -r ws_count tab_count pane_count agent_count working_count < <(
      printf '%s' "$snap" | jq -r '[
        (.result.snapshot.workspaces | length),
        (.result.snapshot.tabs | length),
        (.result.snapshot.panes | length),
        (.result.snapshot.agents | length),
        ([.result.snapshot.agents[]? | select(.agent_status == "working")] | length)
      ] | @tsv' 2>/dev/null || echo "0 0 0 0 0"
    )
  fi

  local load_avg
  load_avg=$(uptime | awk -F'load averages?: ' '{print $2}' || echo "n/a")

  local -a lines=()
  lines+=("${C_BOLD}${C_ACCENT}🌐 CROSS-PROJECT FLEET DASHBOARD & PLUGIN MANAGER${C_RESET}   ${C_PURPLE}[Case 2: placement=full-canvas · Bash PID $PID]${C_RESET}")
  lines+=("${C_MUTED}This plugin occupies the ENTIRE canvas (sidebar hidden) because it operates above individual workspaces.${C_RESET}")
  lines+=("${C_SUBTLE}$(printf '─%.0s' $(seq 1 $((WIDTH - 4))))${C_RESET}")

  lines+=("${C_BOLD}${C_CYAN}FLEET TELEMETRY${C_RESET}   ${C_GREEN}● ${ws_count} Workspaces${C_RESET}  ·  ${C_INK}${tab_count} Tabs${C_RESET}  ·  ${C_INK}${pane_count} Panes${C_RESET}  ·  ${C_YELLOW}${agent_count} Agents (${working_count} working⚡)${C_RESET}  ·  ${C_SUBTLE}Load:${load_avg}${C_RESET}")
  lines+=("")

  lines+=("${C_BOLD}${C_ACCENT}1. CROSS-REPOSITORY WORKSPACE MATRIX (j/k to select · Enter to jump to Space & return to Workbench)${C_RESET}")
  lines+=("  ${C_SUBTLE}$(printf '%-4s  %-18s  %-16s  %-10s  %-8s  %-42s' '#' 'WORKSPACE' 'REPO / WORKTREE' 'STATUS' 'PANES' 'CHECKOUT PATH')${C_RESET}")

  local -a ws_ids=()
  local -a ws_labels=()
  if [[ -n "$snap" ]]; then
    local idx=0
    while IFS=$'\t' read -r wid wnum wlabel wrepo wwt wstatus wpanes wpath; do
      [[ -z "$wid" ]] && continue
      ws_ids+=("$wid")
      ws_labels+=("$wlabel")
      local wt_tag="$wrepo"
      [[ "$wwt" == "true" ]] && wt_tag="${wrepo} (⎇ wt)"
      [[ -z "$wt_tag" ]] && wt_tag="standalone"

      local st_col="$C_MUTED"
      [[ "$wstatus" == "working" ]] && st_col="$C_GREEN"
      [[ "$wstatus" == "blocked" ]] && st_col="$C_YELLOW"

      local row_txt
      row_txt=$(printf '%-4s  %-18s  %-16s  %-10s  %-8s  %-42s' "#$wnum" "${wlabel:0:18}" "${wt_tag:0:16}" "$wstatus" "${wpanes}p" "${wpath:0:42}")
      if (( idx == CURSOR )); then
        lines+=("${C_SEL_BG}${C_BOLD}${C_ACCENT}▎ ${C_INK}${row_txt}${C_RESET}")
      else
        lines+=("  ${st_col}${row_txt}${C_RESET}")
      fi
      idx=$(( idx + 1 ))
    done < <(
      printf '%s' "$snap" | jq -r '
        (.result.snapshot.workspaces // [])[] |
        [
          .workspace_id,
          (.number | tostring),
          .label,
          (.worktree.repo_name // "-"),
          ((.worktree.is_linked_worktree // false) | tostring),
          .agent_status,
          (.pane_count | tostring),
          (.worktree.checkout_path // "/work/lab")
        ] | @tsv
      ' 2>/dev/null
    )
  fi

  local total_ws=${#ws_ids[@]}
  if (( total_ws > 0 && CURSOR >= total_ws )); then
    CURSOR=$(( total_ws - 1 ))
  fi

  lines+=("")
  lines+=("${C_BOLD}${C_PURPLE}2. HUSK OUT-OF-PROCESS PLUGIN PROCESSES (Herdr Process Model)${C_RESET}")
  lines+=("  ${C_SUBTLE}$(printf '%-26s  %-16s  %-16s  %-44s' 'PLUGIN ID' 'LANGUAGE' 'PLACEMENT' 'ROLE')${C_RESET}")
  lines+=("  ${C_GREEN}● dolphin.herdr-sidebar     ${C_INK}Go (Charm)        ${C_CYAN}slots ①-⑦         ${C_MUTED}Live Herdr Spaces, Tree & Agents Sidebar${C_RESET}")
  lines+=("  ${C_GREEN}● bash.tv-search            ${C_INK}Bash + jq         ${C_YELLOW}modal (🔍 F2)     ${C_MUTED}Television / Telescope Session & File Finder${C_RESET}")
  lines+=("  ${C_GREEN}● bash.fleet-dashboard      ${C_INK}Bash + jq         ${C_PURPLE}full-canvas (🌐 F3)${C_MUTED}Cross-Project Fleet & Plugin Manager (Active)${C_RESET}")
  lines+=("  ${C_GREEN}● bash.project-git          ${C_INK}Bash + git        ${C_ACCENT}stage (🌿 F4)     ${C_MUTED}Single-Project Lazygit / Diff Stage${C_RESET}")
  lines+=("")
  lines+=("${C_SUBTLE}Controls: [j/k] Select Workspace · [Enter] Focus Space in Herdr & Exit Full-Canvas · [r] Refresh · [F1 / Esc] Return to Workbench${C_RESET}")

  local json_lines
  json_lines=$(printf '%s\n' "${lines[@]}" | jq -R . | jq -s .)

  jq -cn \
    --arg title "🌐 Cross-Project Fleet & Plugin Hub (Case 2: Full-Canvas)" \
    --arg badge "FULL-CANVAS · BASH" \
    --argjson lines "$json_lines" \
    '{
      type: "slot_update",
      slot_update: {
        plugin_id: "bash.fleet-dashboard",
        slot: "full-canvas",
        title: $title,
        badge: $badge,
        lines: $lines
      }
    }'
}

jump_to_selected_workspace() {
  local snap
  snap=$(printf '{"id":"fleet","method":"session.snapshot","params":{}}\n' | nc -U "$HERDR_SOCK" 2>/dev/null || true)
  [[ -z "$snap" ]] && return
  local target_id target_label
  read -r target_id target_label < <(
    printf '%s' "$snap" | jq -r --argjson idx "$CURSOR" '
      .result.snapshot.workspaces[$idx] | [.workspace_id, .label] | @tsv
    ' 2>/dev/null || true
  )
  if [[ -n "$target_id" && "$target_id" != "null" ]]; then
    printf '{"id":"fleet-f","method":"workspace.focus","params":{"workspace_id":"%s"}}\n' "$target_id" | nc -U "$HERDR_SOCK" >/dev/null 2>&1 || true
    jq -cn --arg lbl "$target_label" '{
      type: "toast",
      close_modal: true,
      toast: {
        plugin_id: "bash.fleet-dashboard",
        level: "success",
        title: "Switched Workspace from Fleet Hub",
        message: ("Focused space " + $lbl + " and returned to Workbench")
      }
    }'
  fi
}

send_hello

while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  msg_type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null || true)
  case "$msg_type" in
    rail_activated)
      WIDTH=$(printf '%s' "$line" | jq -r '.rail_activated.width // 128')
      HEIGHT=$(printf '%s' "$line" | jq -r '.rail_activated.height // 34')
      render_full_canvas
      ;;
    slot_resize)
      w=$(printf '%s' "$line" | jq -r '.slot_resize.slot_sizes["full-canvas"].width // empty' 2>/dev/null || true)
      h=$(printf '%s' "$line" | jq -r '.slot_resize.slot_sizes["full-canvas"].height // empty' 2>/dev/null || true)
      if [[ -n "$w" && -n "$h" ]]; then
        WIDTH="$w"
        HEIGHT="$h"
        render_full_canvas
      fi
      ;;
    slot_key)
      key=$(printf '%s' "$line" | jq -r '.slot_key.key // empty')
      case "$key" in
        j|down)
          CURSOR=$(( CURSOR + 1 ))
          render_full_canvas
          ;;
        k|up)
          (( CURSOR > 0 )) && CURSOR=$(( CURSOR - 1 ))
          render_full_canvas
          ;;
        r)
          render_full_canvas
          ;;
        enter)
          jump_to_selected_workspace
          ;;
      esac
      ;;
  esac
done
