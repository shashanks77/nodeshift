import { runAwsSdkV3Codemod } from './codemods/aws-sdk-v3';
import { runXml2JsonCodemod } from './codemods/xml2json';
import { runUuidCodemod } from './codemods/uuid';

interface CodemodRequest {
  repoPath: string;
  targetVersion: number;
  codemods?: string[];
}

interface CodemodResult {
  name: string;
  success: boolean;
  filesModified: string[];
  error?: string;
}

interface CodemodResponse {
  results: CodemodResult[];
  totalFilesModified: string[];
}

type CodemodFn = (repoPath: string, targetVersion: number) => CodemodResult;

const availableCodemods: Record<string, CodemodFn> = {
  'aws-sdk-v3': runAwsSdkV3Codemod,
  'xml2json': runXml2JsonCodemod,
  'uuid': runUuidCodemod,
};

async function main() {
  const chunks: Buffer[] = [];
  for await (const chunk of process.stdin) {
    chunks.push(chunk as Buffer);
  }
  const input = Buffer.concat(chunks).toString('utf-8');
  const request: CodemodRequest = JSON.parse(input);

  const codemodNames = request.codemods || Object.keys(availableCodemods);
  const results: CodemodResult[] = [];
  const allModified = new Set<string>();

  for (const name of codemodNames) {
    const fn = availableCodemods[name];
    if (!fn) {
      results.push({ name, success: false, filesModified: [], error: `Unknown codemod: ${name}` });
      continue;
    }
    try {
      const result = fn(request.repoPath, request.targetVersion);
      results.push(result);
      result.filesModified.forEach(f => allModified.add(f));
    } catch (err: any) {
      results.push({ name, success: false, filesModified: [], error: err.message });
    }
  }

  const response: CodemodResponse = {
    results,
    totalFilesModified: Array.from(allModified),
  };

  process.stdout.write(JSON.stringify(response));
}

main().catch(err => {
  process.stderr.write(`Codemod engine error: ${err.message}\n`);
  process.exit(1);
});
