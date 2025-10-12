# Agent Working Agreement

This repository expects agents to use advanced search and persistent memory to stay context-aware and precise.

## Core Practices
- Always use advanced code search before implementing changes:
  - Prefer `ripgrep (rg)` locally, or the project’s advanced search tool if available (e.g., `codebase__search_code_advanced`).
  - Build/refresh the deep index at session start to accelerate lookups (e.g., `codebase__build_deep_index`).
  - Search for symbols, keywords, and flows touching the area you’re modifying (config, schema, parser, systems, jobs, queue, worker pool).

- Persist and consult memory for session context:
  - Save important decisions, plans, policies, and follow‑ups in memory so they survive across steps.
  - Re-check memory when switching topics to avoid duplicating work or missing constraints.

- Planning and safety:
  - Maintain a short, living plan and update it as you complete steps.
  - Use buffered reads and chunked file views (<= 250 lines) when inspecting large files.
  - Keep changes minimal and scoped to the task; avoid unrelated refactors.

## Tooling Preferences
- Search: `rg` (or the provided advanced search tool) over `grep`.
- Indexing: build the deep index when starting a new review session.
- Memory: record key context (design choices, thresholds, alerting policy, perf decisions) as you go.

## Commit Discipline
- Only commit when explicitly requested or when the task requires a checkpoint for rollback.
- Use clear, concise commit messages describing the change scope.

