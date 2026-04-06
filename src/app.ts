import express, { Request, Response, NextFunction } from 'express';
import { AppConfig } from './config';
import { AuthManager } from './sap-ai-core/auth';
import { DeploymentManager } from './sap-ai-core/deployments';
import { OpenAIProvider } from './providers/openai';
import { AnthropicOpenAIProvider } from './providers/anthropic-openai';
import { GeminiProvider } from './providers/gemini-openai';
import { AnthropicNativeProvider } from './providers/anthropic-native';
import {
  createHealthRouter,
  createAdminRouter,
  createOpenAICompatibleRouter,
  createAnthropicRouter,
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
  const anthropicOpenAIProvider = new AnthropicOpenAIProvider(authManager, deploymentManager);
  const geminiProvider = new GeminiProvider(authManager, deploymentManager);
  const anthropicNativeProvider = new AnthropicNativeProvider(authManager, deploymentManager);

  // Setup middleware
  setupMiddleware(app);

  // Setup routes via routers
  setupRoutes(app, deploymentManager, openaiProvider, anthropicOpenAIProvider, geminiProvider, anthropicNativeProvider);

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
    res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS');
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
  app.options('*', (_req: Request, res: Response) => {
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
  anthropicOpenAIProvider: AnthropicOpenAIProvider,
  geminiProvider: GeminiProvider,
  anthropicNativeProvider: AnthropicNativeProvider,
): void {
  // Health/info routes at root
  app.use('/', createHealthRouter());

  // OpenAI-compatible proxy
  app.use('/openai-compatible', createOpenAICompatibleRouter(
    deploymentManager, openaiProvider, anthropicOpenAIProvider, geminiProvider,
  ));

  // Anthropic native proxy
  app.use('/anthropic', createAnthropicRouter(anthropicNativeProvider));

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
