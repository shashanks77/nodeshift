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
│   └── verify/               # Post-upgrade verification + audit
├── codemod-engine/           # TypeScript AST codemods (ts-morph)
│   └── src/codemods/
│       ├── aws-sdk-v3.ts     # AWS SDK v2 → v3 migration
│       ├── xml2json.ts       # xml2json → fast-xml-parser
│       └── uuid.ts           # uuid sub-path → named exports
├── bin/                      # Compiled binaries
├── Makefile
└── go.mod
```

## Quick Start

```bash
# Build
make build

# Scan a repo (read-only report)
make scan REPO=/path/to/project

# Upgrade a repo (applies changes + verifies)
make upgrade REPO=/path/to/project

# Specify target Node version (default: 24)
make upgrade REPO=/path/to/project TARGET=22
```

## 4-Phase Pipeline

1. **Phase 1 — AST Codemods**: TypeScript engine transforms code (AWS SDK v2→v3, xml2json, uuid)
2. **Phase 2 — Config Transforms**: Updates Dockerfiles, serverless.yml, .nvmrc, package.json engines/deps
3. **Phase 3 — Verification**: Runs npm install, tsc --noEmit, jest tests
4. **Phase 4 — Vulnerability Scan**: npm audit + auto-fix + before/after comparison

## Features

- **Multi-format detection**: Dockerfiles, serverless.yml, .nvmrc, .node-version, package.json engines, GitHub Actions
- **Dependency analysis**: Flags native modules, EOL packages, incompatible versions
- **AST codemods**: Safe code transforms via ts-morph (not regex)
- **GitHub integration**: Clone, branch, commit, push, create PR with detailed report
- **Lambda support**: Batch process multiple repos via AWS Lambda
