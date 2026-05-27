# Nodeshift

A CLI tool that scans Node.js repositories, detects outdated runtimes and dependencies, and automates upgrades to a target Node.js version. It combines static analysis (EOL packages, version checks, Dockerfile/serverless detection) with optional LLM-powered code fixes (TypeScript errors, test migrations, vulnerability remediation) and creates pull requests with detailed upgrade reports.

## Features

- **Multi-format detection** — Dockerfiles, serverless.yml, .nvmrc, .node-version, package.json engines, GitHub Actions, tsconfig
- **Static dependency analysis** — Flags EOL packages (aws-sdk v2, tslint, request), outdated packages (nats v1, moleculer 0.14, nodemon 2), and NestJS compatibility issues
- **AST codemods** — Safe code transforms via TypeScript engine (AWS SDK v2→v3, uuid, xml2json)
- **LLM-assisted fixes** — Ollama-powered TypeScript error fixing, test migration, and vulnerability remediation
- **Verification pipeline** — npm install → tsc → jest → runtime health check → npm audit
- **GitHub integration** — Clone, branch, commit, push, create/update PR with detailed report
- **Batch processing** — Upgrade multiple repos from a manifest file
- **Scheduled automation** — GitHub Actions workflow runs weekly with optional manual trigger
- **Re-run safe** — Re-running on the same repo updates the existing PR instead of creating duplicates

## Architecture

```
nodeshift/
├── cmd/
│   ├── cli/main.go              # CLI entry point (scan, upgrade, batch)
│   └── lambda/main.go           # AWS Lambda handler for batch processing
├── internal/
│   ├── types.go                 # Shared types (UpgradeReport, VerifySummary)
│   ├── analyzer/
│   │   ├── analyzer.go          # Static dependency analysis rules
│   │   └── llm_analyzer.go      # LLM-based package compatibility analysis
│   ├── detector/                # Node version config detection
│   ├── github/                  # GitHub API client (clone, branch, PR)
│   ├── llm/
│   │   ├── client.go            # Ollama HTTP client
│   │   ├── codemod.go           # LLM-driven code migrations
│   │   ├── fixer.go             # TypeScript/test/vuln auto-fixer
│   │   ├── prompts.go           # Embedded prompt loader
│   │   └── prompts/             # System & user prompt templates
│   ├── transformer/             # Config file transforms (Dockerfile, serverless, etc.)
│   └── verify/
│       ├── verify.go            # npm install + tsc + jest verification
│       └── runtime.go           # Runtime health check (start, probe, shutdown)
├── .github/workflows/
│   └── batch-upgrade.yml        # Scheduled weekly automation
├── repos.json                   # Repository manifest for batch upgrades
├── Makefile
└── go.mod
```

## Quick Start

```bash
# 1. Build
make build

# 2. Scan a repo (read-only report, no changes)
make scan REPO=/path/to/project

# 3. Upgrade a local repo (applies changes + creates PR)
make upgrade REPO=../my-service

# 4. Upgrade a GitHub repo (clones + upgrades + PR)
make upgrade REPO=https://github.com/org/repo.git
```

## Setup

```bash
# Copy env template and add your GitHub token
cp .env.example .env
# Edit .env: GITHUB_TOKEN=ghp_xxxxx

# Build
make build
```

The GitHub token needs the `repo` scope. For org repos with SSO, authorize the token for the organization.

## Commands

| Command | Description |
|---------|-------------|
| `make scan REPO=<path>` | Scan and report issues (no changes) |
| `make upgrade REPO=<path>` | Full upgrade pipeline with PR |
| `make dry-run REPO=<path>` | Apply changes locally, no push/PR |
| `make batch FILE=repos.json` | Batch upgrade multiple repos |
| `make build` | Build CLI binary |
| `make build-lambda` | Build Lambda deployment zip |
| `make deploy` | Deploy Lambda to AWS |
| `make test` | Run Go tests |

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `TARGET` | 24 | Target Node.js version |
| `BASE` | master | Base branch to create upgrade from |
| `FILE` | repos.json | Repo manifest file for batch mode |

### CLI Flags

```bash
./bin/nodeshift upgrade <repo> [flags]
  --target <ver>       Target Node.js version (default: 24)
  --base <branch>      Base branch (default: master)
  --dry-run            Apply locally, skip push/PR
  --codemods           Run AST codemods (AWS SDK v3, etc.)
  --llm-url <url>      Ollama endpoint (default: http://localhost:11434)
  --llm-model <name>   Ollama model (default: qwen2.5-coder:3b)
  --push               Push branch and create PR
```

## Upgrade Pipeline

```
┌─────────────┐    ┌───────────────┐    ┌───────────────┐    ┌────────────────┐
│   Detect    │───▶│    Analyze    │───▶│   Transform   │───▶│    Verify      │
│  Node ver   │    │  dependencies │    │  configs/code │    │ install+tsc+   │
│  configs    │    │  (static+LLM) │    │  (codemods)   │    │ test+runtime   │
└─────────────┘    └───────────────┘    └───────────────┘    └────────────────┘
                                                                      │
                                                               ┌──────▼──────┐
                                                               │  Push + PR  │
                                                               │  (if pass)  │
                                                               └─────────────┘
```

### Phase 1 — Detection & Analysis
- Scans Dockerfiles, serverless.yml, .nvmrc, .node-version, package.json, tsconfig
- Static rules flag: EOL packages, outdated versions, native modules, type mismatches
- Optional LLM analysis for deeper compatibility insights

### Phase 2 — Transform & Codemods
- Updates Dockerfile `FROM node:XX` directives
- Updates serverless.yml runtime versions
- Updates .nvmrc, package.json engines, @types/node, @tsconfig/nodeXX
- AST codemods: AWS SDK v2→v3, uuid, xml2json migrations

### Phase 3 — Verification & Auto-Fix
- `npm install` (with `--legacy-peer-deps` fallback)
- `tsc --noEmit` → LLM auto-fixes TypeScript errors (up to 3 iterations)
- `jest` → LLM auto-fixes test failures (ESM config, mock setup, type changes)
- Runtime health check (start app, probe port, graceful shutdown)
- `npm audit` → LLM-assisted vulnerability remediation

### Phase 4 — Push & PR
- Creates date-based branch: `chore/node-XX-upgrade-YYYY-MM-DD`
- Pushes changes and creates PR with detailed report including:
  - Verification results (install, tsc, tests, runtime, audit)
  - Dependency issues found and resolved
  - What changed (files modified, versions bumped)

## Scheduled Automation

The GitHub Actions workflow (`.github/workflows/batch-upgrade.yml`) runs **daily at 9 AM UTC** and uses group-based scheduling to process only the repos scheduled for that day:

- Repos are organized into **groups** (by team or risk level)
- Each group has a `schedule` field (day of month 1-28) that determines when it runs
- On any given day, only matching groups are processed — keeps runs short and focused
- Manual trigger via GitHub UI can target a specific group or run in scheduled mode

### Scheduling Model

| Day of Month | Group | Repos |
|:---:|--------|-------|
| 1st | `focus` | sf-graphql |
| 2nd | `focus-microservices` | auditing, auth, biodata, content, extracts, insight, notification, subscription, package, tcresponse, user, uploader, micron |
| 3rd | `tci` | tci-focus (disabled) |
| 4th | `dm` | validation, upload, hris |
| 5th | `360` | mfs-api, mfs-reports, scoring |
| 6th | `360-integration` | reporting-service |

### Usage

```bash
# Process only today's scheduled groups
./bin/nodeshift batch --file repos.json --scheduled

# Process a specific group (regardless of schedule)
./bin/nodeshift batch --file repos.json --group fms-low-risk

# Process ALL repos (no filtering)
./bin/nodeshift batch --file repos.json
```

### Manual Trigger (GitHub UI)

| Input | Default | Description |
|-------|---------|-------------|
| `target` | 24 | Target Node.js version |
| `group` | (empty) | Specific group to process; empty = use scheduled mode |
| `dry_run` | false | Preview changes without creating PRs |
| `llm_enabled` | true | Enable AI-assisted code fixes |
| `llm_model` | qwen2.5-coder:7b | Ollama model for LLM fixes |

### Workflow Architecture

```
┌──────────────────────────────────────────────────────────────────────┐
│                GitHub Actions (batch-upgrade.yml)                    │
│                Runs daily at 9 AM UTC                                │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  Trigger: cron (daily 9AM UTC) | workflow_dispatch (manual)          │
│                                                                      │
│  ┌──────────┐  ┌──────────┐  ┌────────────┐  ┌──────────────────┐    │
│  │ Checkout │─▶│ Build Go │─▶│ Setup Node │─▶│ Configure Git ID │    │
│  │   repo   │  │  binary  │  │    v24     │  │ (nodeshift[ai])  │    │
│  └──────────┘  └──────────┘  └────────────┘  └──────────────────┘    │
│                                                       │              │
│                                                       ▼              │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │                Ollama Setup (if LLM enabled)                   │  │
│  │  Install → Start Server → Wait Ready → Pull Model              │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                       │              │
│                                                       ▼              │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │              Group-Based Batch Loop                            │  │
│  │                                                                │  │
│  │  repos.json ──▶ filter by today's schedule ──▶ for each repo:  │  │
│  │                                                                │  │
│  │  ┌───────┐  ┌─────────┐  ┌───────────┐  ┌────────┐  ┌─────┐    │  │
│  │  │ Clone │─▶│  Scan   │─▶│ Transform │─▶│ Verify │─▶│ PR  │    │  │
│  │  │       │  │+Analyze │  │ +Codemods │  │+LLM Fix│  │     │    │  │
│  │  └───────┘  └─────────┘  └───────────┘  └────────┘  └─────┘    │  │
│  │                                                                │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                                                                      │
└──────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼ (for each repo in scheduled group)
        ┌──────────────────────────────────────────────┐
        │            Pull Request Created              │
        │                                              │
        │  Branch: chore/node-24-upgrade-2026-05-27    │
        │  Title:  chore: upgrade Node.js to 24        │
        │  Body:   Verification results table          │
        │          Dependency issues resolved          │
        │          Files changed summary               │
        └──────────────────────────────────────────────┘
```

## Static Detection Rules

### EOL Packages (always flagged)
| Package | Replacement |
|---------|-------------|
| `aws-sdk` (v2) | `@aws-sdk/*` (v3 modular clients) |
| `request` | axios, undici, or native fetch |
| `tslint` | eslint + @typescript-eslint |
| `serverless-pseudo-parameters` | Native `${aws:*}` syntax |

### Outdated Packages (version check)
| Package | Minimum | Reason |
|---------|---------|--------|
| `nodemon` | 3.x | Compat issues with Node 20+ |
| `nats` | 2.x | v1 deprecated, v2 uses JetStream |
| `moleculer` | 0.15 | 0.14 outdated, no Node 20+ patches |
| `serverless-offline` | 14.x | Required for Serverless v3 + Node 20+ |
| `@nestjs/core` | 10.x | <10 incompatible with Node 20+ |
| `@nestjs/config` | 3.x | <3 uses removed `util.isObject()` |

## Repo Manifest (repos.json)

Supports group-based scheduling for company-scale deployments:

```json
[
  {
    "group": "team-name",
    "schedule": 1,
    "repos": [
      { "url": "https://github.com/org/repo1.git", "baseBranch": "main" },
      { "url": "https://github.com/org/repo2.git", "baseBranch": "develop" },
      { "url": "https://github.com/org/repo3.git", "baseBranch": "main", "disabled": true }
    ]
  },
  {
    "group": "another-team",
    "schedule": 15,
    "repos": [
      { "url": "https://github.com/org/repo4.git", "baseBranch": "master" }
    ]
  }
]
```

| Field | Description |
|-------|-------------|
| `group` | A label for the group. Can be anything (team name, project name, etc.). Used only for `--group` filtering and logging — not referenced by the workflow. |
| `schedule` | Day of month (1-28) when this group should be processed. The workflow runs daily and only picks up groups matching today's date. |
| `repos[].url` | GitHub clone URL |
| `repos[].baseBranch` | Branch to base the upgrade on |
| `repos[].disabled` | Set `true` to skip a repo without removing it |

The flat format (array of repos without groups) is still supported for backward compatibility.

### Adding New Repos

To add a new repository to the scheduled upgrades:

1. Open `repos.json`
2. Find the group it belongs to (or create a new group)
3. Add an entry to the `repos` array:

```json
{ "url": "https://github.com/org/new-repo.git", "baseBranch": "main" }
```

To add a new team/group:

```json
{
  "group": "new-team",
  "schedule": 22,
  "repos": [
    { "url": "https://github.com/org/repo-a.git", "baseBranch": "main" },
    { "url": "https://github.com/org/repo-b.git", "baseBranch": "develop" }
  ]
}
```

Pick a `schedule` day (1-28) that doesn't overlap with existing groups to spread the load across the month. Multiple groups can share the same day if the combined repo count stays under the workflow timeout (~60 repos at ~5 min each = 5 hours max).

## LLM Integration

Uses Ollama for local inference. The LLM assists with:
- **Package analysis** — Identifies compatibility issues beyond static rules
- **Codemod generation** — Generates code transformations for API migrations
- **TypeScript fixes** — Resolves type errors introduced by version bumps
- **Test fixes** — Updates test mocks and assertions for new SDK versions
- **Vulnerability fixes** — Applies security patches from audit findings

Prompts are stored in `internal/llm/prompts/` as embedded text templates.

## Requirements

- Go 1.25+
- Node.js (for verification steps)
- GitHub token with `repo` scope
- Ollama (optional, for LLM features)
