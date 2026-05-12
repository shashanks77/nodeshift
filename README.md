# Nodeshift

Automated Node.js version upgrade agent. Scans repositories for Node version configs, detects dependency compatibility issues, applies AST-level codemods, and verifies the upgrade with npm install, TypeScript compilation, tests, and vulnerability scanning.

## Architecture

```
nodeshift/
├── cmd/
│   ├── cli/main.go          # CLI entry point (scan + upgrade commands)
│   └── lambda/main.go       # AWS Lambda handler for batch processing
├── internal/
│   ├── types.go              # Shared types
│   ├── analyzer/             # Dependency compatibility analysis
│   ├── codemod/              # Bridge to TypeScript codemod engine
│   ├── detector/             # Node version config detection
│   ├── github/               # GitHub API client (clone, branch, PR)
│   ├── transformer/          # Config file transforms
│   └── verify/               # Post-upgrade verification + auto-fix + audit
├── codemod-engine/           # TypeScript AST codemods (ts-morph)
│   └── src/codemods/
│       ├── aws-sdk-v3.ts     # AWS SDK v2 → v3 migration
│       ├── xml2json.ts       # xml2json → fast-xml-parser
│       └── uuid.ts           # uuid sub-path → named exports
├── .env                      # GitHub token (gitignored)
├── .env.example              # Template for .env
├── Makefile
└── go.mod
```

## Quick Start

```bash
# Build
make build

# Scan a repo (read-only report)
make scan REPO=/path/to/project

# Upgrade a GitHub repo (clone + upgrade + PR)
make upgrade REPO=https://github.com/SHL-India/FOCUS-tci-focus.git

# Upgrade a local repo (upgrades + pushes PR to its GitHub remote)
make upgrade REPO=../tci-focus

# Specify target Node version (default: 24)
make upgrade REPO=../tci-focus TARGET=22

# Specify base branch (default: master)
make upgrade REPO=../tci-focus BASE=develop

# Dry run (apply changes locally, no push/PR)
./bin/nodeshift upgrade ../tci-focus --dry-run --codemods
```

## Setup

```bash
# 1. Copy env template and add your GitHub token
cp .env.example .env
# Edit .env and set GITHUB_TOKEN=ghp_xxxxx

# 2. Build
make build
```

The GitHub token needs the `repo` scope. For org repos with SSO, authorize the token for the organization.

## 4-Phase Pipeline

1. **Phase 1 — AST Codemods**: TypeScript engine transforms code (AWS SDK v2→v3, xml2json, uuid) across `src/`, `test/`, `lib/`
2. **Phase 2 — Config Transforms**: Updates Dockerfiles, serverless.yml, .nvmrc, package.json engines/deps
3. **Phase 3 — Verification**:
   - `npm install` (with `--legacy-peer-deps` fallback)
   - `tsc --noEmit` → auto-fix errors (duplicate identifiers, type mismatches, non-null assertions)
   - `jest` tests → auto-fix failures (ESM config, AWS SDK v3 mocks, test imports/types/assertions)
   - Hanging promise detection and restructuring
4. **Phase 4 — Vulnerability Scan**: npm audit + auto-fix + before/after comparison

## Auto-Fix Capabilities

### TypeScript Errors (tsc)
- TS2300: Duplicate identifiers from double imports
- TS2345: `ReturnValues` string → `as const`
- TS2345: `string | undefined` → non-null assertion

### Test Failures (jest)
- Patches jest config with `transformIgnorePatterns` for ESM packages (`@aws-sdk`, `@smithy`)
- Creates AWS SDK v3 mock setup (`SmithyClient.prototype.send`) with command-aware responses
- Fixes sub-path aws-sdk imports in test files
- Replaces v2 type names with v3 equivalents
- Removes `.promise()` calls and `jest.mock('aws-sdk')`
- Fixes hanging promises (Promise wrappers around dangling `.send()` calls)

## Features

- **Multi-format detection**: Dockerfiles, serverless.yml, .nvmrc, .node-version, package.json engines, GitHub Actions
- **Dependency analysis**: Flags native modules, EOL packages, incompatible versions
- **AST codemods**: Safe code transforms via ts-morph (not regex)
- **Test validation**: Runs tests and auto-fixes failures after upgrade
- **GitHub integration**: Clone, branch, commit, push, create PR with detailed report
- **Re-run safe**: Re-running on the same repo updates the existing PR instead of creating duplicates
- **Lambda support**: Batch process multiple repos via AWS Lambda
