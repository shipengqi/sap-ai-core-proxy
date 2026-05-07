import express, { Request, Response, NextFunction } from 'express';
import { AppConfig } from './config';
import { AuthManager } from './sap-ai-core/auth';
import { DeploymentManager } from './sap-ai-core/deployments';
import { OpenAIProvider, ClaudeOpenAIProvider, GeminiProvider, ClaudeAnthropicProvider, EmbeddingsProvider, ResponsesProvider, AudioProvider } from './providers';
import {
  createHealthRouter,
  createAdminRouter,
  createOpenAICompatibleRouter,
  createAnthropicRouter,
  createClaudeCodeCompatRouter,
  buildProviderRegistry,
} from './routers';
import { logger } from './logger';

interface AppResult {
  readonly app: express.Application;
  readonly authManager: AuthManager;
  readonly deploymentManager: DeploymentManager;
}

/**
 * Creates and configures the Express application
 */
export function createApp(config: AppConfig): AppResult {
  const app = express();

  // Initialize managers
  const authManager = new AuthManager(config.credentials);
  const deploymentManager = new DeploymentManager(authManager);

  // Initialize providers
  const openaiProvider = new OpenAIProvider(authManager, deploymentManager);
  const claudeOpenAIProvider = new ClaudeOpenAIProvider(authManager, deploymentManager);
  const geminiProvider = new GeminiProvider(authManager, deploymentManager);
  const claudeAnthropicProvider = new ClaudeAnthropicProvider(authManager, deploymentManager);
  const embeddingsProvider = new EmbeddingsProvider(authManager, deploymentManager);
  const responsesProvider = new ResponsesProvider(authManager, deploymentManager);
  const audioProvider = new AudioProvider(authManager, deploymentManager);

  // Setup middleware
  setupMiddleware(app);

  // Setup routes via routers
  setupRoutes(app, deploymentManager, openaiProvider, claudeOpenAIProvider, geminiProvider, claudeAnthropicProvider, embeddingsProvider, responsesProvider, audioProvider);

  return { app, authManager, deploymentManager };
}

/**
 * Sets up Express middleware
 */
function setupMiddleware(app: express.Application): void {
  // Parse JSON bodies
  app.use(express.json({ limit: '50mb' }));

  // Request logging
  app.use((req: Request, _res: Response, next: NextFunction) => {
    logger.request(req.method, req.path, req.body);
    next();
  });

  // CORS headers - include all headers Claude Code CLI/VSCode extension may send
  app.use((_req: Request, res: Response, next: NextFunction) => {
    res.setHeader('Access-Control-Allow-Origin', '*');
    res.setHeader('Access-Control-Allow-Methods', 'GET, POST, HEAD, OPTIONS');
    res.setHeader('Access-Control-Allow-Headers', [
      'Content-Type',
      'Authorization',
      'x-api-key',
      'anthropic-version',
      'anthropic-beta',
      'anthropic-dangerous-direct-browser-access',
      'x-app',
      'x-stainless-arch',
      'x-stainless-helper-method',
      'x-stainless-lang',
      'x-stainless-os',
      'x-stainless-package-version',
      'x-stainless-runtime',
      'x-stainless-runtime-version',
    ].join(', '));
    next();
  });

  // Handle OPTIONS requests
  app.options('*path', (_req: Request, res: Response) => {
    res.status(204).end();
  });
}

/**
 * Sets up API routes using Express Routers
 */
function setupRoutes(
  app: express.Application,
  deploymentManager: DeploymentManager,
  openaiProvider: OpenAIProvider,
  claudeOpenAIProvider: ClaudeOpenAIProvider,
  geminiProvider: GeminiProvider,
  claudeAnthropicProvider: ClaudeAnthropicProvider,
  embeddingsProvider: EmbeddingsProvider,
  responsesProvider: ResponsesProvider,
  audioProvider: AudioProvider,
): void {
  // Health/info routes at root
  app.use('/', createHealthRouter());

  // OpenAI surface
  const providerRegistry = buildProviderRegistry(claudeOpenAIProvider, geminiProvider);
  app.use('/openai', createOpenAICompatibleRouter({
    deploymentManager,
    providerRegistry,
    defaultHandler: openaiProvider.handleChatCompletion.bind(openaiProvider),
    embeddingsProvider,
    responsesProvider,
    audioProvider,
  }));

  // Anthropic surface
  app.use('/anthropic', createAnthropicRouter(claudeAnthropicProvider, deploymentManager));

  // Claude Code CLI / VSCode extension compatibility shim
  app.use('/anthropic', createClaudeCodeCompatRouter());

  // Admin routes
  app.use('/admin', createAdminRouter(deploymentManager));

  // Error handling
  app.use((err: Error, _req: Request, res: Response, _next: NextFunction) => {
    logger.error('Unhandled error:', err.message);
    res.status(500).json({
      error: {
        message: err.message || 'Internal server error',
        type: 'server_error',
        param: null,
        code: '500',
      },
    });
  });

  // 404 handler
  app.use((req: Request, res: Response) => {
    logger.warn(`Unknown endpoint: ${req.method} ${req.path}`);
    res.status(404).json({
      error: {
        message: `Endpoint not found: ${req.method} ${req.path}`,
        type: 'invalid_request_error',
        param: null,
        code: '404',
      },
    });
  });
}
