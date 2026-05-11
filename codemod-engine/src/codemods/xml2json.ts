import { Project } from 'ts-morph';
import * as path from 'path';
import { globSync } from 'glob';

interface CodemodResult {
  name: string;
  success: boolean;
  filesModified: string[];
  error?: string;
}

export function runXml2JsonCodemod(repoPath: string, _targetVersion: number): CodemodResult {
  const result: CodemodResult = { name: 'xml2json', success: true, filesModified: [] };

  const pkgPath = path.join(repoPath, 'package.json');
  let pkgJson: any;
  try {
    pkgJson = JSON.parse(require('fs').readFileSync(pkgPath, 'utf-8'));
  } catch {
    return result;
  }

  const allDeps = { ...pkgJson.dependencies, ...pkgJson.devDependencies };
  if (!allDeps['xml2json']) {
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

  if (files.length === 0) return result;

  const project = new Project({
    compilerOptions: { allowJs: true, noEmit: true },
    skipAddingFilesFromTsConfig: true,
  });

  for (const file of files) {
    project.addSourceFileAtPath(file);
  }

  for (const sourceFile of project.getSourceFiles()) {
    let text = sourceFile.getFullText();
    let changed = false;

    // Replace require('xml2json') with require('fast-xml-parser')
    if (text.includes('xml2json')) {
      // Replace import/require
      text = text.replace(
        /require\s*\(\s*['"]xml2json['"]\s*\)/g,
        "require('fast-xml-parser')"
      );
      text = text.replace(
        /from\s+['"]xml2json['"]/g,
        "from 'fast-xml-parser'"
      );

      // Replace xml2json.toJson(xmlString) with parser.parse(xmlString)
      text = text.replace(
        /(\w+)\.toJson\s*\(/g,
        'new (require("fast-xml-parser").XMLParser)().parse('
      );

      changed = true;
    }

    if (changed) {
      sourceFile.replaceWithText(text);
      sourceFile.saveSync();
      result.filesModified.push(path.relative(repoPath, sourceFile.getFilePath()));
    }
  }

  return result;
}
