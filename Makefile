BINARY_NAME=nodeshift
LAMBDA_BINARY=bootstrap
GOOS_LINUX=linux
GOARCH=amd64

# Load .env file if it exists
-include .env
export

.PHONY: build build-lambda deploy clean scan upgrade test help

# Show available commands
help:
	@echo "Nodeshift - Make Commands"
	@echo "========================"
	@echo ""
	@echo "  make build              Build the CLI binary"
	@echo "  make scan REPO=<path>   Scan repo and show upgrade report (no changes)"
	@echo "  make upgrade REPO=<path> Reset, upgrade, and verify (npm i + tsc)"
	@echo "  make test               Run Go tests"
	@echo "  make build-lambda       Build Lambda deployment zip"
	@echo "  make deploy             Deploy Lambda to AWS"
	@echo ""
	@echo "Options:"
	@echo "  REPO=<path>   Path to the target repository (required for scan/upgrade)"
	@echo "  TARGET=<ver>  Target Node.js version (default: 24)"
	@echo ""
	@echo "Examples:"
	@echo "  make scan REPO=/path/to/my-project"
	@echo "  make upgrade REPO=/path/to/my-project TARGET=22"

# Build CLI binary for local machine
build:
	go build -o bin/$(BINARY_NAME) ./cmd/cli

# Build Lambda binary (Linux AMD64, static)
build-lambda:
	GOOS=$(GOOS_LINUX) GOARCH=$(GOARCH) CGO_ENABLED=0 \
		go build -ldflags="-s -w" -o bin/$(LAMBDA_BINARY) ./cmd/lambda
	cd bin && zip lambda.zip $(LAMBDA_BINARY)

# Run scan against a local repo (report only, no changes)
scan:
	./bin/$(BINARY_NAME) scan --local $(REPO) --target $(or $(TARGET),24)

# Run upgrade with codemods + verification (npm install + tsc)
upgrade:
	cd $(REPO) && git checkout -- . && cd - > /dev/null && \
	./bin/$(BINARY_NAME) upgrade --local $(REPO) --target $(or $(TARGET),24) --codemods

# Run upgrade on a remote GitHub repo (clone + upgrade + PR)
upgrade-remote:
	./bin/$(BINARY_NAME) upgrade --owner $(OWNER) --repo $(REPO) --target $(or $(TARGET),24) --base $(or $(BASE),master) --codemods

# Run tests
test:
	go test ./...

# Deploy Lambda via AWS CLI
deploy: build-lambda
	aws lambda update-function-code \
		--function-name nodeshift \
		--zip-file fileb://bin/lambda.zip

# Create Lambda function (first time only)
create-lambda: build-lambda
	aws lambda create-function \
		--function-name nodeshift \
		--runtime provided.al2023 \
		--handler bootstrap \
		--role $(LAMBDA_ROLE_ARN) \
		--zip-file fileb://bin/lambda.zip \
		--timeout 300 \
		--memory-size 256 \
		--environment "Variables={GITHUB_TOKEN=$(GITHUB_TOKEN),GITHUB_OWNER=$(GITHUB_OWNER),GITHUB_REPOS=$(GITHUB_REPOS),TARGET_NODE_VERSION=24}"

# Create EventBridge schedule (weekly Monday 9AM UTC)
create-schedule:
	aws events put-rule \
		--name nodeshift-weekly \
		--schedule-expression "cron(0 9 ? * MON *)" \
		--state ENABLED
	aws events put-targets \
		--rule nodeshift-weekly \
		--targets "Id"="1","Arn"="$(LAMBDA_ARN)"

# Clean build artifacts
clean:
	rm -rf bin/
