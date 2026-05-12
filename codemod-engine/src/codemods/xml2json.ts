import * as path from 'path';
import * as fs from 'fs';
import { globSync } from 'glob';

interface CodemodResult {
  name: string;
  success: boolean;
  filesModified: string[];
  error?: string;
}

/**
 * Codemod: replace xml2json and xml-to-json-stream with fast-xml-parser.
 *
 * Handles two packages:
 *   1. xml2json        — obj.toJson(xml)  → new XMLParser().parse(xml)
 *   2. xml-to-json-stream — callback API  → synchronous XMLParser().parse(xml)
 *
 * Also patches package.json: removes old deps, adds fast-xml-parser,
 * and removes the xml-to-json-stream type declaration if present.
 */
export function runXml2JsonCodemod(repoPath: string, _targetVersion: number): CodemodResult {
  const result: CodemodResult = { name: 'xml2json', success: true, filesModified: [] };

  const pkgPath = path.join(repoPath, 'package.json');
  let pkgJson: any;
  try {
    pkgJson = JSON.parse(fs.readFileSync(pkgPath, 'utf-8'));
  } catch {
    return result;
  }

  const allDeps = { ...pkgJson.dependencies, ...pkgJson.devDependencies };
  const hasXml2json = !!allDeps['xml2json'];
  const hasXmlToJsonStream = !!allDeps['xml-to-json-stream'];

  if (!hasXml2json && !hasXmlToJsonStream) {
    return result;
  }

  // ── Source file transforms ──

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

  for (const filePath of files) {
    let text = fs.readFileSync(filePath, 'utf-8');
    const original = text;

    // ── xml2json package (toJson API) ──
    if (hasXml2json && text.includes('xml2json')) {
      text = text.replace(
        /require\s*\(\s*['"]xml2json['"]\s*\)/g,
        "require('fast-xml-parser')"
      );
      text = text.replace(
        /from\s+['"]xml2json['"]/g,
        "from 'fast-xml-parser'"
      );
      text = text.replace(
        /(\w+)\.toJson\s*\(/g,
        'new (require("fast-xml-parser").XMLParser)().parse('
      );
    }

    // ── xml-to-json-stream package (callback API) ──
    if (hasXmlToJsonStream) {
      // Replace: import xmlToJson from 'xml-to-json-stream'
      //   with:  import { XMLParser } from 'fast-xml-parser'
      text = text.replace(
        /import\s+(\w+)\s+from\s+['"]xml-to-json-stream['"]\s*;?/g,
        "import { XMLParser } from 'fast-xml-parser';"
      );
      // Replace: const xmlToJson = require('xml-to-json-stream')
      text = text.replace(
        /(?:const|let|var)\s+(\w+)\s*=\s*require\s*\(\s*['"]xml-to-json-stream['"]\s*\)\s*;?/g,
        "const { XMLParser } = require('fast-xml-parser');"
      );

      // Replace parser creation:
      //   const parser = xmlToJson({ attributeMode: true })   → const parser = new XMLParser({ ignoreAttributes: false })
      //   const parser = xmlToJson({ attributeMode: false })  → const parser = new XMLParser({ ignoreAttributes: true })
      text = text.replace(
        /(\w+)\(\s*\{\s*attributeMode\s*:\s*true\s*\}\s*\)/g,
        'new XMLParser({ ignoreAttributes: false, parseTagValue: false })'
      );
      text = text.replace(
        /(\w+)\(\s*\{\s*attributeMode\s*:\s*false\s*\}\s*\)/g,
        'new XMLParser({ ignoreAttributes: true, parseTagValue: false })'
      );

      // Replace callback-based parsing with synchronous try/catch.
      //
      // Pattern in source files (return + callback):
      //   return parser.xmlToJson(xml, (err: any, json: string) => {
      //     if (err) { console.error(...); throw ...; }
      //     return json;
      //   });
      // → try { return parser.parse(xml); } catch (err) { console.error(...); throw ...; }
      //
      // Pattern in test files (await + callback with body):
      //   await parser.xmlToJson(data, (err: any, json: Type) => {
      //     if (err) { ... }
      //     // body using json
      //   });
      // → const json: Type = parser.parse(data); // body using json

      // Strategy: find the function containing parser.xmlToJson and rewrite
      // the whole return/await statement. We use a balanced-brace approach.
      text = rewriteXmlToJsonCalls(text);
    }

    if (text !== original) {
      fs.writeFileSync(filePath, text, 'utf-8');
      result.filesModified.push(path.relative(repoPath, filePath));
    }
  }

  // ── package.json: swap dependencies ──
  let pkgChanged = false;
  if (hasXml2json && pkgJson.dependencies?.['xml2json']) {
    delete pkgJson.dependencies['xml2json'];
    pkgChanged = true;
  }
  if (hasXml2json && pkgJson.devDependencies?.['xml2json']) {
    delete pkgJson.devDependencies['xml2json'];
    pkgChanged = true;
  }
  if (hasXmlToJsonStream && pkgJson.dependencies?.['xml-to-json-stream']) {
    delete pkgJson.dependencies['xml-to-json-stream'];
    pkgChanged = true;
  }
  if (hasXmlToJsonStream && pkgJson.devDependencies?.['xml-to-json-stream']) {
    delete pkgJson.devDependencies['xml-to-json-stream'];
    pkgChanged = true;
  }
  // Add fast-xml-parser if not already present
  if (!allDeps['fast-xml-parser']) {
    pkgJson.dependencies = pkgJson.dependencies || {};
    pkgJson.dependencies['fast-xml-parser'] = '^4.3.0';
    pkgChanged = true;
  }
  if (pkgChanged) {
    fs.writeFileSync(pkgPath, JSON.stringify(pkgJson, null, 2) + '\n', 'utf-8');
    result.filesModified.push('package.json');
  }

  // ── Remove type declaration for xml-to-json-stream if present ──
  const declPath = path.join(repoPath, 'src', 'types', 'xml-to-json-stream.d.ts');
  if (fs.existsSync(declPath)) {
    fs.unlinkSync(declPath);
    result.filesModified.push('src/types/xml-to-json-stream.d.ts');
  }

  return result;
}

/**
 * Find all `.xmlToJson(xml, callback)` calls and rewrite to synchronous `.parse(xml)`.
 * Uses brace-balancing that respects string/template literals.
 */
function rewriteXmlToJsonCalls(text: string): string {
  const callRe = /(\w+)\.xmlToJson\s*\(/g;
  let match: RegExpExecArray | null;
  let output = '';
  let lastIdx = 0;

  while ((match = callRe.exec(text)) !== null) {
    const parserVar = match[1];
    const argsStart = match.index + match[0].length;

    const argsStr = extractBalanced(text, argsStart, '(', ')');
    if (argsStr === null) continue;

    const fullCallEnd = argsStart + argsStr.length + 1;

    // Parse header: xmlArg, (err: any, json: Type) => {
    const headerMatch = argsStr.match(
      /^(\w+)\s*,\s*\((\w+)\s*:\s*any\s*,\s*(\w+)\s*(?::\s*(\w+))?\)\s*=>\s*\{/
    );
    if (!headerMatch) continue;

    const [headerStr, xmlArg, errArg, jsonArg, jsonType] = headerMatch;

    // The callback body is between the `{` after `=>` and the last `}` of argsStr
    const bodyStart = headerStr.length;
    const callbackBody = argsStr.slice(bodyStart, argsStr.length - 1);

    // Find `if (err) { ... }` inside the callback body using brace balancing
    const ifMatch = callbackBody.match(/if\s*\(\s*\w+\s*\)\s*\{/);
    let errBody = '';
    let afterErrBlock = '';
    if (ifMatch && ifMatch.index !== undefined) {
      const ifBraceStart = ifMatch.index + ifMatch[0].length;
      const ifContent = extractBalanced(callbackBody, ifBraceStart, '{', '}');
      if (ifContent !== null) {
        errBody = ifContent.trim();
        afterErrBlock = callbackBody.slice(ifBraceStart + ifContent.length + 1).trim();
        // Remove trailing "return json;" from afterErrBlock
        afterErrBlock = afterErrBlock.replace(/return\s+\w+\s*;?\s*$/, '').trim();
      }
    }

    // Determine calling context
    const before = text.slice(Math.max(0, match.index - 30), match.index);
    const isReturn = /return\s+$/.test(before);
    const isAwait = /await\s+$/.test(before);

    let replacement: string;
    let startIdx = match.index;

    if ((isReturn || (!isAwait && !afterErrBlock)) && errBody) {
      // return parser.xmlToJson(...) → try/catch
      replacement = `try {\n    return ${parserVar}.parse(${xmlArg});\n  } catch (${errArg}) {\n    ${errBody}\n  }`;
      if (isReturn) {
        const retIdx = text.lastIndexOf('return', match.index);
        if (retIdx >= 0 && retIdx > lastIdx) startIdx = retIdx;
      }
    } else if (isAwait || afterErrBlock) {
      // await parser.xmlToJson(...) → const json = parser.parse(...)
      const typeAnnotation = jsonType ? `: ${jsonType}` : '';
      const body = afterErrBlock || '';
      replacement = `const ${jsonArg}${typeAnnotation} = ${parserVar}.parse(${xmlArg});`;
      if (body) replacement += `\n    ${body}`;
      if (isAwait) {
        const awaitIdx = text.lastIndexOf('await', match.index);
        if (awaitIdx >= 0 && awaitIdx > lastIdx) startIdx = awaitIdx;
      }
    } else {
      replacement = `${parserVar}.parse(${xmlArg})`;
    }

    output += text.slice(lastIdx, startIdx);
    lastIdx = fullCallEnd;
    output += replacement;
  }

  output += text.slice(lastIdx);
  return output;
}

/**
 * Extract balanced content between open/close brackets, respecting
 * string literals (', ", `) and template literal expressions (${...}).
 * `start` should point to the character right after the opening bracket.
 * Returns content between brackets (exclusive), or null if unbalanced.
 */
function extractBalanced(text: string, start: number, open: string, close: string): string | null {
  let depth = 1;
  let i = start;
  while (i < text.length && depth > 0) {
    const ch = text[i];
    if (ch === '"' || ch === "'" || ch === '`') {
      // Skip quoted strings
      const quote = ch;
      i++;
      while (i < text.length) {
        if (text[i] === '\\') { i += 2; continue; }
        if (quote === '`' && text[i] === '$' && i + 1 < text.length && text[i + 1] === '{') {
          // Recurse into template expression
          i += 2;
          let td = 1;
          while (i < text.length && td > 0) {
            if (text[i] === '\\') { i += 2; continue; }
            if (text[i] === '{') td++;
            else if (text[i] === '}') td--;
            if (td > 0) i++;
          }
          if (i < text.length) i++; // skip closing }
          continue;
        }
        if (text[i] === quote) { i++; break; }
        i++;
      }
      continue;
    }
    if (ch === open) depth++;
    else if (ch === close) depth--;
    if (depth > 0) i++;
  }
  if (depth !== 0) return null;
  return text.slice(start, i);
}
