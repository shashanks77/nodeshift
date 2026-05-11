import * as path from 'path';
import * as fs from 'fs';
import { globSync } from 'glob';

interface CodemodResult {
  name: string;
  success: boolean;
  filesModified: string[];
  error?: string;
}

export function runUuidCodemod(repoPath: string, _targetVersion: number): CodemodResult {
  const result: CodemodResult = { name: 'uuid', success: true, filesModified: [] };

  const pkgPath = path.join(repoPath, 'package.json');
  let pkgJson: any;
  try {
    pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf-8'));
  } catch {
    return result;
  }

  const allDeps = { ...pkgJson.dependencies, ...pkgJson.devDependencies };
  if (!allDeps['uuid']) {
    return result;
  }

  const patterns = [
    path.join(repoPath, 'src', '**', '*.{ts,js}'),
    path.join(repoPath, 'lib', '**', '*.{ts,js}'),
    path.join(repoPath, 'tests', '**', '*.{ts,js}'),
    path.join(repoPath, 'test', '**', '*.{ts,js}'),
  ];

  const files: string[] = [];
  for (const pattern of patterns) {
    files.push(...globSync(pattern, { ignore: ['**/node_modules/**'] }));
  }

  for (const file of files) {
    const content = fs.readFileSync(file, 'utf-8');
    let updated = content;

    // Replace uuid/v4 or uuid/v1 default imports with named imports
    updated = updated.replace(
      /(?:const|let|var)\s+(\w+)\s*=\s*require\s*\(\s*['"]uuid\/v4['"]\s*\)/g,
      "const { v4: $1 } = require('uuid')"
    );
    updated = updated.replace(
      /(?:const|let|var)\s+(\w+)\s*=\s*require\s*\(\s*['"]uuid\/v1['"]\s*\)/g,
      "const { v1: $1 } = require('uuid')"
    );

    // Replace ES module imports
    updated = updated.replace(
      /import\s+(\w+)\s+from\s+['"]uuid\/v4['"]/g,
      "import { v4 as $1 } from 'uuid'"
    );
    updated = updated.replace(
      /import\s+(\w+)\s+from\s+['"]uuid\/v1['"]/g,
      "import { v1 as $1 } from 'uuid'"
    );

    if (updated !== content) {
      fs.writeFileSync(file, updated, 'utf-8');
      result.filesModified.push(path.relative(repoPath, file));
    }
  }

  return result;
}
