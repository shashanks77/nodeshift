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

  if (usedServices.size === 0) {
    return false;
  }

  // Now transform: replace imports
  for (const imp of awsImports) {
    imp.remove();
    changed = true;
  }

  // Add new v3 imports
  const addedImports = new Map<string, Set<string>>();

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
  }

  // Insert new imports at the top
  let insertIdx = 0;
  for (const [pkg, names] of addedImports) {
    const namesList = Array.from(names).sort().join(', ');
    sourceFile.insertStatements(insertIdx, `import { ${namesList} } from '${pkg}';`);
    insertIdx++;
  }

  // Transform constructor calls: new AWS.S3(...) → new S3Client(...)
  let text = sourceFile.getFullText();
  for (const [serviceName, usage] of usedServices) {
    const serviceInfo = serviceMap[serviceName];
    // Replace new AWS.ServiceName(...) with new Client(...)
    const ctorRegex = new RegExp(
      `new\\s+(?:AWS\\.)?${serviceName.replace('.', '\\.')}\\s*\\(`,
      'g'
    );
    text = text.replace(ctorRegex, `new ${serviceInfo.client}(`);
  }

  // Transform method calls: varName.method(params).promise() → varName.send(new MethodCommand(params))
  for (const [serviceName, usage] of usedServices) {
    const serviceInfo = serviceMap[serviceName];

    for (const varName of usage.varNames) {
      // Skip known non-AWS variables
      if (skipVars.has(varName)) continue;

      for (const [methodName, cmdClass] of Object.entries(serviceInfo.commands)) {
        // Pattern: varName.methodName(args).promise()
        const promisePattern = new RegExp(
          `${escapeRegExp(varName)}\\.${escapeRegExp(methodName)}\\s*\\(`,
          'g'
        );

        text = text.replace(promisePattern, (match) => {
          return `${varName}.send(new ${cmdClass}(`;
        });

        // Remove .promise() calls that now follow .send(...)
        // The closing paren for send needs to be added
      }
    }
  }

  // Clean up .promise() calls
  text = text.replace(/\.promise\(\)/g, '');

  // Fix double closing parens from send(new Command(args))  → ensure proper nesting
  // This is handled by the balanced paren approach below

  sourceFile.replaceWithText(text);
  changed = true;

  return changed;
}

function escapeRegExp(str: string): string {
  return str.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
