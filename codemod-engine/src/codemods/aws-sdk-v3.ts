import { Project, SyntaxKind, SourceFile, CallExpression, Node } from 'ts-morph';
import * as path from 'path';
import { globSync } from 'glob';

interface CodemodResult {
  name: string;
  success: boolean;
  filesModified: string[];
  error?: string;
}

// Maps AWS SDK v2 service names to v3 package and client names
const serviceMap: Record<string, { pkg: string; client: string; commands: Record<string, string> }> = {
  DynamoDB: {
    pkg: '@aws-sdk/client-dynamodb',
    client: 'DynamoDBClient',
    commands: {
      getItem: 'GetItemCommand',
      putItem: 'PutItemCommand',
      updateItem: 'UpdateItemCommand',
      deleteItem: 'DeleteItemCommand',
      query: 'QueryCommand',
      scan: 'ScanCommand',
      batchWriteItem: 'BatchWriteItemCommand',
      batchGetItem: 'BatchGetItemCommand',
      createTable: 'CreateTableCommand',
      deleteTable: 'DeleteTableCommand',
      describeTable: 'DescribeTableCommand',
      listTables: 'ListTablesCommand',
      transactWriteItems: 'TransactWriteItemsCommand',
    },
  },
  'DynamoDB.DocumentClient': {
    pkg: '@aws-sdk/lib-dynamodb',
    client: 'DynamoDBDocumentClient',
    commands: {
      get: 'GetCommand',
      put: 'PutCommand',
      update: 'UpdateCommand',
      delete: 'DeleteCommand',
      query: 'QueryCommand',
      scan: 'ScanCommand',
      batchWrite: 'BatchWriteCommand',
      batchGet: 'BatchGetCommand',
      transactWrite: 'TransactWriteCommand',
    },
  },
  S3: {
    pkg: '@aws-sdk/client-s3',
    client: 'S3Client',
    commands: {
      getObject: 'GetObjectCommand',
      putObject: 'PutObjectCommand',
      deleteObject: 'DeleteObjectCommand',
      listObjectsV2: 'ListObjectsV2Command',
      headObject: 'HeadObjectCommand',
      copyObject: 'CopyObjectCommand',
      createBucket: 'CreateBucketCommand',
      deleteBucket: 'DeleteBucketCommand',
      upload: 'PutObjectCommand',
    },
  },
  SQS: {
    pkg: '@aws-sdk/client-sqs',
    client: 'SQSClient',
    commands: {
      sendMessage: 'SendMessageCommand',
      sendMessageBatch: 'SendMessageBatchCommand',
      receiveMessage: 'ReceiveMessageCommand',
      deleteMessage: 'DeleteMessageCommand',
      deleteMessageBatch: 'DeleteMessageBatchCommand',
      getQueueUrl: 'GetQueueUrlCommand',
      createQueue: 'CreateQueueCommand',
      deleteQueue: 'DeleteQueueCommand',
      purgeQueue: 'PurgeQueueCommand',
    },
  },
  SNS: {
    pkg: '@aws-sdk/client-sns',
    client: 'SNSClient',
    commands: {
      publish: 'PublishCommand',
      publishBatch: 'PublishBatchCommand',
      subscribe: 'SubscribeCommand',
      unsubscribe: 'UnsubscribeCommand',
      createTopic: 'CreateTopicCommand',
      deleteTopic: 'DeleteTopicCommand',
      listTopics: 'ListTopicsCommand',
    },
  },
  SecretsManager: {
    pkg: '@aws-sdk/client-secrets-manager',
    client: 'SecretsManagerClient',
    commands: {
      getSecretValue: 'GetSecretValueCommand',
      createSecret: 'CreateSecretCommand',
      updateSecret: 'UpdateSecretCommand',
      deleteSecret: 'DeleteSecretCommand',
      listSecrets: 'ListSecretsCommand',
    },
  },
  Lambda: {
    pkg: '@aws-sdk/client-lambda',
    client: 'LambdaClient',
    commands: {
      invoke: 'InvokeCommand',
      createFunction: 'CreateFunctionCommand',
      deleteFunction: 'DeleteFunctionCommand',
      updateFunctionCode: 'UpdateFunctionCodeCommand',
      listFunctions: 'ListFunctionsCommand',
    },
  },
  StepFunctions: {
    pkg: '@aws-sdk/client-sfn',
    client: 'SFNClient',
    commands: {
      startExecution: 'StartExecutionCommand',
      describeExecution: 'DescribeExecutionCommand',
      stopExecution: 'StopExecutionCommand',
      listExecutions: 'ListExecutionsCommand',
      listStateMachines: 'ListStateMachinesCommand',
    },
  },
};

// Maps v2 type names to v3 equivalents
const v2TypeMap: Record<string, { replacement: string; pkg: string }> = {
  'PublishInput': { replacement: 'PublishCommandInput', pkg: '@aws-sdk/client-sns' },
  'PublishResponse': { replacement: 'PublishCommandOutput', pkg: '@aws-sdk/client-sns' },
  'SendMessageRequest': { replacement: 'SendMessageCommandInput', pkg: '@aws-sdk/client-sqs' },
  'SendMessageResult': { replacement: 'SendMessageCommandOutput', pkg: '@aws-sdk/client-sqs' },
  'GetItemInput': { replacement: 'GetItemCommandInput', pkg: '@aws-sdk/client-dynamodb' },
  'GetItemOutput': { replacement: 'GetItemCommandOutput', pkg: '@aws-sdk/client-dynamodb' },
  'PutItemInput': { replacement: 'PutItemCommandInput', pkg: '@aws-sdk/client-dynamodb' },
  'QueryInput': { replacement: 'QueryCommandInput', pkg: '@aws-sdk/client-dynamodb' },
  'ScanInput': { replacement: 'ScanCommandInput', pkg: '@aws-sdk/client-dynamodb' },
  'GetObjectRequest': { replacement: 'GetObjectCommandInput', pkg: '@aws-sdk/client-s3' },
  'PutObjectRequest': { replacement: 'PutObjectCommandInput', pkg: '@aws-sdk/client-s3' },
  'GetSecretValueRequest': { replacement: 'GetSecretValueCommandInput', pkg: '@aws-sdk/client-secrets-manager' },
  'GetSecretValueResponse': { replacement: 'GetSecretValueCommandOutput', pkg: '@aws-sdk/client-secrets-manager' },
  'InvocationRequest': { replacement: 'InvokeCommandInput', pkg: '@aws-sdk/client-lambda' },
  'InvocationResponse': { replacement: 'InvokeCommandOutput', pkg: '@aws-sdk/client-lambda' },
};

// Maps aws-sdk/clients/xxx sub-path imports to v3 packages
function mapSubPathToV3Pkg(mod: string): string | null {
  const match = mod.match(/^aws-sdk\/clients\/(.+)$/);
  if (match) {
    const svcName = match[1].toLowerCase();
    // Handle known aliases
    const aliases: Record<string, string> = {
      'stepfunctions': 'sfn',
      'secretsmanager': 'secrets-manager',
    };
    return `@aws-sdk/client-${aliases[svcName] || svcName}`;
  }
  return null;
}

// Variable names that should never be treated as AWS SDK instances
const skipVars = new Set([
  'console', 'Math', 'JSON', 'Object', 'Array', 'Promise', 'Date',
  'axios', 'http', 'https', 'request', 'response', 'res', 'req',
  'app', 'router', 'server', 'express',
  'cacheSvc', 'cache', 'cacheService', 'cacheManager',
  'module', 'testModule', 'moduleRef',
  'this', 'self',
  'map', 'set', 'array', 'list',
  'logger', 'log',
  'config', 'configService',
  'db', 'database', 'connection', 'pool', 'client',
  'redis', 'redisClient',
  'mock', 'stub', 'spy',
]);

export function runAwsSdkV3Codemod(repoPath: string, _targetVersion: number): CodemodResult {
  const result: CodemodResult = { name: 'aws-sdk-v3', success: true, filesModified: [] };

  // Check if aws-sdk is even used
  const pkgPath = path.join(repoPath, 'package.json');
  let pkgJson: any;
  try {
    pkgJson = JSON.parse(require('fs').readFileSync(pkgPath, 'utf-8'));
  } catch {
    return result; // no package.json, skip
  }

  const allDeps = { ...pkgJson.dependencies, ...pkgJson.devDependencies };
  if (!allDeps['aws-sdk']) {
    return result; // aws-sdk not used, skip
  }

  // Find all TS/JS source files
  const patterns = [
    path.join(repoPath, 'src', '**', '*.{ts,js}'),
    path.join(repoPath, 'lib', '**', '*.{ts,js}'),
    path.join(repoPath, 'tests', '**', '*.{ts,js}'),
    path.join(repoPath, 'test', '**', '*.{ts,js}'),
    path.join(repoPath, '__tests__', '**', '*.{ts,js}'),
  ];

  const files: string[] = [];
  for (const pattern of patterns) {
    files.push(...globSync(pattern, { ignore: ['**/node_modules/**'] }));
  }

  if (files.length === 0) {
    return result;
  }

  const project = new Project({
    compilerOptions: { allowJs: true, noEmit: true },
    skipAddingFilesFromTsConfig: true,
  });

  for (const file of files) {
    project.addSourceFileAtPath(file);
  }

  for (const sourceFile of project.getSourceFiles()) {
    const changed = transformSourceFile(sourceFile, repoPath);
    if (changed) {
      sourceFile.saveSync();
      result.filesModified.push(path.relative(repoPath, sourceFile.getFilePath()));
    }
  }

  return result;
}

function transformSourceFile(sourceFile: SourceFile, repoPath: string): boolean {
  let changed = false;

  // Track which AWS services are used in this file
  const usedServices = new Map<string, { varNames: Set<string>; commandsUsed: Set<string> }>();

  // Find import declarations for aws-sdk
  const imports = sourceFile.getImportDeclarations();
  const awsImports = imports.filter(i => {
    const mod = i.getModuleSpecifierValue();
    return mod === 'aws-sdk' || mod.startsWith('aws-sdk/');
  });

  if (awsImports.length === 0) {
    return false;
  }

  // Analyze which services are instantiated
  const fullText = sourceFile.getFullText();

  for (const [serviceName, serviceInfo] of Object.entries(serviceMap)) {
    // Check if this service is used in the file
    if (!fullText.includes(serviceName.split('.').pop()!)) {
      continue;
    }

    const entry = usedServices.get(serviceName) || { varNames: new Set<string>(), commandsUsed: new Set<string>() };

    // Find `new AWS.ServiceName(...)` or `new ServiceName(...)` patterns
    const constructorRegex = new RegExp(
      `(?:new\\s+(?:AWS\\.)?${serviceName.replace('.', '\\.')}\\s*\\()`,
      'g'
    );
    if (constructorRegex.test(fullText)) {
      // Find variable names assigned to this constructor
      const assignRegex = new RegExp(
        `(?:const|let|var)\\s+(\\w+)\\s*=\\s*new\\s+(?:AWS\\.)?${serviceName.replace('.', '\\.')}`,
        'g'
      );
      let match;
      while ((match = assignRegex.exec(fullText)) !== null) {
        entry.varNames.add(match[1]);
      }

      // Also check this.xxx = new AWS.ServiceName(...)
      const thisAssignRegex = new RegExp(
        `this\\.(\\w+)\\s*=\\s*new\\s+(?:AWS\\.)?${serviceName.replace('.', '\\.')}`,
        'g'
      );
      while ((match = thisAssignRegex.exec(fullText)) !== null) {
        entry.varNames.add(match[1]);
      }
    }

    // Find which methods are called on these variables
    for (const varName of entry.varNames) {
      for (const methodName of Object.keys(serviceInfo.commands)) {
        const methodRegex = new RegExp(`${varName}\\.${methodName}\\s*\\(`, 'g');
        if (methodRegex.test(fullText)) {
          entry.commandsUsed.add(methodName);
        }
      }
    }

    if (entry.varNames.size > 0 || entry.commandsUsed.size > 0) {
      usedServices.set(serviceName, entry);
    }
  }

  // Check for sub-path imports like `import { PublishInput } from 'aws-sdk/clients/sns'`
  const subPathImports = awsImports.filter(i => {
    const mod = i.getModuleSpecifierValue();
    return mod.startsWith('aws-sdk/clients/') || mod.startsWith('aws-sdk/lib/');
  });

  if (usedServices.size === 0 && subPathImports.length === 0) {
    return false;
  }

  // Build unified import map for all v3 imports
  const addedImports = new Map<string, Set<string>>();

  // Collect types from sub-path imports, then remove them
  for (const imp of subPathImports) {
    const mod = imp.getModuleSpecifierValue();
    const v3Pkg = mapSubPathToV3Pkg(mod);
    if (v3Pkg) {
      const importSet = addedImports.get(v3Pkg) || new Set<string>();
      for (const named of imp.getNamedImports()) {
        const oldName = named.getName();
        const mapping = v2TypeMap[oldName];
        importSet.add(mapping ? mapping.replacement : oldName);
      }
      // Handle default imports (e.g., import SecretsManager from 'aws-sdk/clients/secretsmanager')
      const defaultImport = imp.getDefaultImport();
      if (defaultImport) {
        // v2 default import is the service class; map to v3 client
        const svcName = mod.replace('aws-sdk/clients/', '');
        for (const [, svc] of Object.entries(serviceMap)) {
          if (svc.pkg === v3Pkg) {
            importSet.add(svc.client);
            break;
          }
        }
      }
      addedImports.set(v3Pkg, importSet);
      imp.remove();
      changed = true;
    }
  }

  // Remove main aws-sdk imports
  for (const imp of awsImports.filter(i => !subPathImports.includes(i))) {
    imp.remove();
    changed = true;
  }

  // Add service-based imports
  for (const [serviceName, usage] of usedServices) {
    const serviceInfo = serviceMap[serviceName];
    if (!serviceInfo) continue;

    const importSet = addedImports.get(serviceInfo.pkg) || new Set<string>();
    importSet.add(serviceInfo.client);

    for (const cmdName of usage.commandsUsed) {
      const cmdClass = serviceInfo.commands[cmdName];
      if (cmdClass) {
        importSet.add(cmdClass);
      }
    }

    addedImports.set(serviceInfo.pkg, importSet);

    // DynamoDB.DocumentClient also needs DynamoDBClient from client-dynamodb
    if (serviceName === 'DynamoDB.DocumentClient') {
      const dynamoImportSet = addedImports.get('@aws-sdk/client-dynamodb') || new Set<string>();
      dynamoImportSet.add('DynamoDBClient');
      addedImports.set('@aws-sdk/client-dynamodb', dynamoImportSet);
    }
  }

  // Insert new imports at the top
  let insertIdx = 0;
  for (const [pkg, names] of addedImports) {
    const namesList = Array.from(names).sort().join(', ');
    sourceFile.insertStatements(insertIdx, `import { ${namesList} } from '${pkg}';`);
    insertIdx++;
  }

  // If only sub-path imports existed (no service constructors), just do type replacement
  if (usedServices.size === 0) {
    let text = sourceFile.getFullText();
    for (const [v2Type, { replacement }] of Object.entries(v2TypeMap)) {
      text = text.replace(new RegExp(`\\b${v2Type}\\b`, 'g'), replacement);
    }
    text = text.replace(/\.promise\(\)/g, '');
    sourceFile.replaceWithText(text);
    return changed;
  }

  // Transform constructor calls: new AWS.S3(...) → new S3Client(...)
  // Special case: DynamoDB.DocumentClient needs DynamoDBDocumentClient.from(new DynamoDBClient(...))
  let text = sourceFile.getFullText();
  for (const [serviceName, usage] of usedServices) {
    const serviceInfo = serviceMap[serviceName];

    if (serviceName === 'DynamoDB.DocumentClient') {
      // new AWS.DynamoDB.DocumentClient({...}) → DynamoDBDocumentClient.from(new DynamoDBClient({...}))
      const ctorRegex = new RegExp(
        `new\\s+(?:AWS\\.)?DynamoDB\\.DocumentClient\\s*\\(`,
        'g'
      );
      // Find each occurrence and do balanced-paren replacement
      let match;
      const replacements: { start: number; end: number; replacement: string }[] = [];
      while ((match = ctorRegex.exec(text)) !== null) {
        const argsStart = match.index + match[0].length;
        const closeIdx = findBalancedParen(text, argsStart);
        if (closeIdx === -1) continue;
        const args = text.substring(argsStart, closeIdx).trim();
        const replacement = `DynamoDBDocumentClient.from(new DynamoDBClient(${args}))`;
        replacements.push({ start: match.index, end: closeIdx + 1, replacement });
      }
      for (let i = replacements.length - 1; i >= 0; i--) {
        const r = replacements[i];
        text = text.substring(0, r.start) + r.replacement + text.substring(r.end);
      }
    } else {
      const ctorRegex = new RegExp(
        `new\\s+(?:AWS\\.)?${serviceName.replace('.', '\\.')}\\s*\\(`,
        'g'
      );
      text = text.replace(ctorRegex, `new ${serviceInfo.client}(`);
    }
  }

  // Transform method calls: varName.method(params).promise() → varName.send(new MethodCommand(params))
  for (const [serviceName, usage] of usedServices) {
    const serviceInfo = serviceMap[serviceName];

    for (const varName of usage.varNames) {
      if (skipVars.has(varName)) continue;

      for (const [methodName, cmdClass] of Object.entries(serviceInfo.commands)) {
        // Find and replace each call site with balanced paren matching
        const pattern = new RegExp(
          `${escapeRegExp(varName)}\\.${escapeRegExp(methodName)}\\s*\\(`,
          'g'
        );

        let match;
        const replacements: { start: number; end: number; replacement: string }[] = [];

        while ((match = pattern.exec(text)) !== null) {
          const callStart = match.index;
          const argsStart = callStart + match[0].length;

          // Find balanced closing paren
          const closeIdx = findBalancedParen(text, argsStart);
          if (closeIdx === -1) continue;

          // Extract the args (without the outer parens)
          let args = text.substring(argsStart, closeIdx).trim();

          // Strip callback function arguments: (params, function(err, data) {...})
          // Keep only the first argument (the params object)
          args = stripCallback(args);

          // Check what follows the closing paren
          let endIdx = closeIdx + 1; // past the ')'

          // Strip .promise() if present
          const afterClose = text.substring(endIdx);
          const promiseMatch = afterClose.match(/^\s*\.promise\s*\(\s*\)/);
          if (promiseMatch) {
            endIdx += promiseMatch[0].length;
          }

          const replacement = `${varName}.send(new ${cmdClass}(${args}))`;
          replacements.push({ start: callStart, end: endIdx, replacement });
        }

        // Apply replacements in reverse order to preserve indices
        for (let i = replacements.length - 1; i >= 0; i--) {
          const r = replacements[i];
          text = text.substring(0, r.start) + r.replacement + text.substring(r.end);
        }
      }
    }
  }

  // Clean up any remaining .promise() calls
  text = text.replace(/\.promise\(\)/g, '');

  // Replace v2 type names with v3 equivalents in code text
  for (const [v2Type, { replacement, pkg }] of Object.entries(v2TypeMap)) {
    const typeRegex = new RegExp(`\\b${v2Type}\\b`, 'g');
    if (typeRegex.test(text)) {
      text = text.replace(typeRegex, replacement);
      // Add the type to imports
      const importMatch = text.match(new RegExp(`from\\s+['"]${escapeRegExp(pkg)}['"]`));
      if (importMatch) {
        // Add the type to the existing import
        const importRegex = new RegExp(`(import\\s*\\{[^}]*)(}\\s*from\\s*['"]${escapeRegExp(pkg)}['"])`);
        const existingMatch = text.match(importRegex);
        if (existingMatch && !existingMatch[1].includes(replacement)) {
          text = text.replace(importRegex, `$1, ${replacement} $2`);
        }
      }
    }
  }

  sourceFile.replaceWithText(text);
  changed = true;

  return changed;
}

/**
 * Find the index of the balanced closing paren starting from position `start`
 * (where `start` points to the character after the opening paren).
 */
function findBalancedParen(text: string, start: number): number {
  let depth = 1;
  let i = start;
  while (i < text.length && depth > 0) {
    const ch = text[i];
    if (ch === '(') depth++;
    else if (ch === ')') depth--;
    if (depth === 0) return i;
    // Skip string literals
    if (ch === "'" || ch === '"' || ch === '`') {
      i = skipString(text, i, ch);
      continue;
    }
    i++;
  }
  return -1;
}

function skipString(text: string, start: number, quote: string): number {
  let i = start + 1;
  while (i < text.length) {
    if (text[i] === '\\') { i += 2; continue; }
    if (text[i] === quote) return i + 1;
    if (quote === '`' && text[i] === '$' && text[i + 1] === '{') {
      // Template literal expression - skip balanced braces
      i += 2;
      let depth = 1;
      while (i < text.length && depth > 0) {
        if (text[i] === '{') depth++;
        else if (text[i] === '}') depth--;
        i++;
      }
      continue;
    }
    i++;
  }
  return i;
}

/**
 * Strip callback arguments from AWS SDK v2 calls.
 * e.g., "params, function(err, data) { ... }" → "params"
 * e.g., "params, (err, data) => { ... }" → "params"
 */
function stripCallback(args: string): string {
  // Match trailing callback: , function(...) { ... } or , (...) => { ... }
  // We need to find a comma followed by function/arrow at the top level
  let depth = 0;
  for (let i = 0; i < args.length; i++) {
    const ch = args[i];
    if (ch === '(' || ch === '{' || ch === '[') depth++;
    else if (ch === ')' || ch === '}' || ch === ']') depth--;
    else if (ch === "'" || ch === '"' || ch === '`') {
      i = skipString(args, i, ch) - 1;
      continue;
    }
    else if (depth === 0 && ch === ',') {
      // Check if what follows is a callback
      const rest = args.substring(i + 1).trim();
      if (rest.match(/^function\s*\(/) || rest.match(/^\([\w\s,]*\)\s*=>/)) {
        return args.substring(0, i).trim();
      }
    }
  }
  return args;
}

function escapeRegExp(str: string): string {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
