#!/usr/bin/env bash
set -u

PID=$$
WIDTH=92
HEIGHT=34
CURRENT_CWD="$PWD"
CURRENT_WS_ID="demo"
CURRENT_WS_LABEL="Demo"
CURRENT_REPO=""
SUBTAB="status" # "status", "log", "worktrees"

C_RESET=$'\033[0m'
C_BOLD=$'\033[1m'
C_ACCENT=$'\033[38;5;63m'
C_PURPLE=$'\033[38;5;135m'
C_GREEN=$'\033[38;5;10m'
C_YELLOW=$'\033[38;5;11m'
C_RED=$'\033[38;5;9m'
C_CYAN=$'\033[38;5;14m'
C_INK=$'\033[38;5;15m'
C_MUTED=$'\033[38;5;7m'
C_SUBTLE=$'\033[38;5;245m'
C_PILL_BG=$'\033[48;5;238m'

send_hello() {
  jq -cn --arg pid "$PID" '{
    type: "hello",
    hello: {
      plugin_id: "bash.project-git",
      name: "Lazygit / Project Git Stage (Bash)",
      version: "0.1.0",
      language: "Bash + git",
      pid: ($pid | tonumber),
      description: "Case 3 (Stage minus Sidebar): Single-project Git/Worktree stage reacting to sidebar selection",
      slots: ["workspace-stage"],
      rail_items: [
        {
          id: "rail.project-git",
          plugin_id: "bash.project-git",
          icon: "",
          label: "Lazygit Stage",
          shortcut: "f4",
          placement: "stage",
          description: "Case 3 (Canvas minus Sidebar): Lazygit / gh-dash style project view for selected space"
        }
      ],
      commands: [
        {
          id: "gitstage.open",
          title: "Project Git (Bash Stage): Open Lazygit-Style Project View (Canvas minus Sidebar)",
          category: "Rail Plugins",
          shortcut: "f4 / ctrl+e",
          description: "Uses the canvas area minus the sidebar, updating live when you switch spaces"
        }
      ]
    }
  }'
}

render_stage() {
  local target_dir="$CURRENT_CWD"
  if [[ ! -d "$target_dir" ]]; then
    target_dir="$PWD"
  fi

  local git_root=""
  git_root=$(cd "$target_dir" 2>/dev/null && git rev-parse --show-toplevel 2>/dev/null || true)

  local branch="detached"
  local head_sha="n/a"
  if [[ -n "$git_root" ]]; then
    branch=$(cd "$git_root" && git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "main")
    head_sha=$(cd "$git_root" && git rev-parse --short HEAD 2>/dev/null || echo "n/a")
  fi

  local tab_s=" [s] Working Tree & Diff "
  local tab_l=" [l] Commit Graph "
  local tab_w=" [w] Git Worktrees "
  local inactive_col="$C_SUBTLE"
  if [[ "${ALT_HELD:-false}" == "true" ]]; then
    inactive_col="${C_BOLD}${C_YELLOW}"
  fi
  [[ "$SUBTAB" == "status" ]] && tab_s="${C_PILL_BG}${C_BOLD}${C_ACCENT}${tab_s}${C_RESET}" || tab_s="${inactive_col}${tab_s}${C_RESET}"
  [[ "$SUBTAB" == "log" ]] && tab_l="${C_PILL_BG}${C_BOLD}${C_ACCENT}${tab_l}${C_RESET}" || tab_l="${inactive_col}${tab_l}${C_RESET}"
  [[ "$SUBTAB" == "worktrees" ]] && tab_w="${C_PILL_BG}${C_BOLD}${C_ACCENT}${tab_w}${C_RESET}" || tab_w="${inactive_col}${tab_w}${C_RESET}"

  local -a lines=()
  lines+=("${C_BOLD}${C_GREEN}🌿 PROJECT GIT & WORKTREE STAGE (LAZYGIT MODE)${C_RESET}   ${C_PURPLE}[Case 3: placement=stage · Bash PID $PID]${C_RESET}")
  lines+=("${C_MUTED}Uses canvas minus sidebar — select any Space in the left sidebar and this view updates to that project!${C_RESET}")
  lines+=("${C_SUBTLE}$(printf '─%.0s' $(seq 1 $((WIDTH - 4))))${C_RESET}")
  lines+=("${C_BOLD}${C_INK}Active Space:${C_RESET} ${C_ACCENT}${CURRENT_WS_LABEL} (${CURRENT_WS_ID})${C_RESET}   ·   ${C_BOLD}${C_INK}Branch:${C_RESET} ${C_GREEN}⎇ ${branch} (${head_sha})${C_RESET}   ·   ${C_BOLD}${C_INK}CWD:${C_RESET} ${C_MUTED}${target_dir}${C_RESET}")
  lines+=("${tab_s}   ${tab_l}   ${tab_w}")
  lines+=("${C_SUBTLE}$(printf '─%.0s' $(seq 1 $((WIDTH - 4))))${C_RESET}")

  if [[ -z "$git_root" ]]; then
    lines+=("${C_YELLOW}  Directory ${target_dir} is not inside a Git repository.${C_RESET}")
  elif [[ "$SUBTAB" == "status" ]]; then
    lines+=("${C_BOLD}${C_CYAN}WORKING TREE STATUS (${git_root})${C_RESET}")
    local st_out
    st_out=$(cd "$git_root" && git status --short 2>/dev/null | head -n 8 || true)
    if [[ -z "$st_out" ]]; then
      lines+=("  ${C_GREEN}✓ Working tree clean — no uncommitted changes${C_RESET}")
    else
      while IFS= read -r sline; do
        lines+=("  ${C_YELLOW}${sline}${C_RESET}")
      done <<< "$st_out"
    fi
    lines+=("")
    lines+=("${C_BOLD}${C_PURPLE}RECENT COMMITS ON ${branch}${C_RESET}")
    while IFS= read -r lline; do
      [[ -z "$lline" ]] && continue
      lines+=("  ${C_INK}${lline:0:$((WIDTH - 6))}${C_RESET}")
    done < <(cd "$git_root" && git log --oneline --decorate -n 8 2>/dev/null || true)
  elif [[ "$SUBTAB" == "log" ]]; then
    lines+=("${C_BOLD}${C_PURPLE}COMMIT GRAPH (${git_root})${C_RESET}")
    while IFS= read -r gline; do
      [[ -z "$gline" ]] && continue
      lines+=("  ${C_INK}${gline:0:$((WIDTH - 6))}${C_RESET}")
    done < <(cd "$git_root" && git log --graph --oneline --decorate -n 16 2>/dev/null || true)
  else
    lines+=("${C_BOLD}${C_CYAN}LINKED GIT WORKTREES (${git_root})${C_RESET}")
    while IFS= read -r wline; do
      [[ -z "$wline" ]] && continue
      lines+=("  ${C_GREEN}⎇ ${C_INK}${wline:0:$((WIDTH - 8))}${C_RESET}")
    done < <(cd "$git_root" && git worktree list 2>/dev/null || true)
  fi

  lines+=("")
  lines+=("${C_SUBTLE}Controls: [s] Status+Log · [l] Graph · [w] Worktrees · [r] Refresh · Switch Space in Left Sidebar · [F1 / Esc] Default Stage${C_RESET}")

  local json_lines
  json_lines=$(printf '%s\n' "${lines[@]}" | jq -R . | jq -s .)

  jq -cn \
    --arg title "🌿 Project Git Stage · ${CURRENT_WS_LABEL} (${branch})" \
    --arg badge "STAGE-MINUS-SIDEBAR · BASH" \
    --argjson lines "$json_lines" \
    '{
      type: "slot_update",
      slot_update: {
        plugin_id: "bash.project-git",
        slot: "workspace-stage",
        title: $title,
        badge: $badge,
        lines: $lines
      }
    }'
}

send_hello

while IFS= read -r line; do
  [[ -z "$line" ]] && continue
  msg_type=$(printf '%s' "$line" | jq -r '.type // empty' 2>/dev/null || true)
  case "$msg_type" in
    welcome)
      CURRENT_CWD=$(printf '%s' "$line" | jq -r --arg fb "$PWD" '.welcome.context.cwd // $fb')
      CURRENT_WS_ID=$(printf '%s' "$line" | jq -r '.welcome.context.workspace_id // "demo"')
      CURRENT_WS_LABEL=$(printf '%s' "$line" | jq -r '.welcome.context.workspace_label // "Demo"')
      ALT_HELD=$(printf '%s' "$line" | jq -r '.welcome.modifiers.alt // false')
      ;;
    key_state)
      ALT_HELD=$(printf '%s' "$line" | jq -r '.key_state.modifiers.alt // false')
      render_stage
      ;;
    workspace_context)
      CURRENT_CWD=$(printf '%s' "$line" | jq -r --arg fb "$PWD" '.workspace_context.cwd // $fb')
      CURRENT_WS_ID=$(printf '%s' "$line" | jq -r '.workspace_context.workspace_id // "demo"')
      CURRENT_WS_LABEL=$(printf '%s' "$line" | jq -r '.workspace_context.workspace_label // "Demo"')
      CURRENT_REPO=$(printf '%s' "$line" | jq -r '.workspace_context.repo_name // ""')
      render_stage
      ;;
    rail_activated)
      WIDTH=$(printf '%s' "$line" | jq -r '.rail_activated.width // 92')
      HEIGHT=$(printf '%s' "$line" | jq -r '.rail_activated.height // 34')
      c_cwd=$(printf '%s' "$line" | jq -r '.rail_activated.context.cwd // empty')
      c_lbl=$(printf '%s' "$line" | jq -r '.rail_activated.context.workspace_label // empty')
      c_id=$(printf '%s' "$line" | jq -r '.rail_activated.context.workspace_id // empty')
      [[ -n "$c_cwd" ]] && CURRENT_CWD="$c_cwd"
      [[ -n "$c_lbl" ]] && CURRENT_WS_LABEL="$c_lbl"
      [[ -n "$c_id" ]] && CURRENT_WS_ID="$c_id"
      render_stage
      ;;
    slot_resize)
      w=$(printf '%s' "$line" | jq -r '.slot_resize.slot_sizes["workspace-stage"].width // empty' 2>/dev/null || true)
      h=$(printf '%s' "$line" | jq -r '.slot_resize.slot_sizes["workspace-stage"].height // empty' 2>/dev/null || true)
      if [[ -n "$w" && -n "$h" ]]; then
        WIDTH="$w"
        HEIGHT="$h"
        render_stage
      fi
      ;;
    slot_key)
      key=$(printf '%s' "$line" | jq -r '.slot_key.key // empty')
      case "$key" in
        s)
          SUBTAB="status"
          render_stage
          ;;
        l)
          SUBTAB="log"
          render_stage
          ;;
        w)
          SUBTAB="worktrees"
          render_stage
          ;;
        r)
          render_stage
          ;;
      esac
      ;;
  esac
done
