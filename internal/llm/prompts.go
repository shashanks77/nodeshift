package llm

import _ "embed"

//go:embed prompts/system_tsc_fix.txt
var SystemPromptTscFix string

//go:embed prompts/system_vuln_fix.txt
var SystemPromptVulnFix string

//go:embed prompts/user_tsc_fix.txt
var PromptTscFix string

//go:embed prompts/user_test_fix.txt
var PromptTestFix string

//go:embed prompts/user_vuln_fix.txt
var PromptVulnFix string

//go:embed prompts/system_codemod.txt
var SystemPromptCodemod string

//go:embed prompts/user_codemod.txt
var PromptCodemod string
