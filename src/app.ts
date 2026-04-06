import express, { Request, Response, NextFunction } from 'express';
import { AppConfig } from './config';
import { AuthManager } from './sap-ai-core/auth';
import { DeploymentManager } from './sap-ai-core/deployments';
import { OpenAIProvider } from './providers/openai';
import { AnthropicOpenAIProvider } from './providers/anthropic-openai';
import { GeminiProvider } from './providers/gemini';
import { AnthropicNativeProvider } from './providers/anthropic-native';
import { handleListModels, handleGetModel } from './routes/models';
import { handleChatCompletions } from './routes/chat-completions';
import { createMessagesHandlers } from './routes/messages';
import { setupClaudeCodeAuthRoutes } from './routes/claude-code-auth';
import { logger } from './logger';

/**
 * Creates and configures the Express application
 */
export function createApp(config: AppConfig): express.Application {
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

  // Setup routes
  setupRoutes(app, deploymentManager, openaiProvider, anthropicOpenAIProvider, geminiProvider, anthropicNativeProvider);

  return app;
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

  // CORS headers
  app.use((_req: Request, res: Response, next: NextFunction) => {
    res.setHeader('Access-Control-Allow-Origin', '*');
    res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS');
    res.setHeader('Access-Control-Allow-Headers', 'Content-Type, Authorization');
    next();
  });

  // Handle OPTIONS requests
  app.options('*', (_req: Request, res: Response) => {
    res.status(204).end();
  });
}

/**
 * Sets up API routes
 */
function setupRoutes(
  app: express.Application,
  deploymentManager: DeploymentManager,
  openaiProvider: OpenAIProvider,
  anthropicOpenAIProvider: AnthropicOpenAIProvider,
  geminiProvider: GeminiProvider,
  anthropicNativeProvider: AnthropicNativeProvider,
): void {
  // Root endpoint - API info
  app.get('/', (_req: Request, res: Response) => {
    res.json({
      name: 'SAP AI Core Proxy',
      version: '1.0.0',
      status: 'ok',
      endpoints: {
        models: '/v1/models',
        chat: '/v1/chat/completions',
        health: '/health',
      },
    });
  });

  // Health check
  app.get('/health', (_req: Request, res: Response) => {
    res.json({ status: 'ok', timestamp: new Date().toISOString() });
  });

  // Model endpoints
  app.get('/v1/models', handleListModels(deploymentManager));
  app.get('/models', handleListModels(deploymentManager));
  app.get('/v1/models/:modelId', handleGetModel(deploymentManager));

  // Chat completions
  app.post('/v1/chat/completions', handleChatCompletions(deploymentManager, openaiProvider, anthropicOpenAIProvider, geminiProvider));
  app.post('/chat/completions', handleChatCompletions(deploymentManager, openaiProvider, anthropicOpenAIProvider, geminiProvider));

  // Anthropic Messages API endpoints (for Claude Code CLI / VSCode extension)
  const messagesHandlers = createMessagesHandlers(anthropicNativeProvider);
  app.post('/v1/messages', messagesHandlers.handleMessages);
  app.post('/v1/messages/count_tokens', messagesHandlers.handleCountTokens);

  // Claude Code auth stub endpoints
  setupClaudeCodeAuthRoutes(app);

  // Refresh deployments endpoint
  app.post('/admin/refresh-deployments', async (_req: Request, res: Response) => {
    try {
      const deployments = await deploymentManager.refreshDeployments();
      res.json({
        success: true,
        count: deployments.length,
        deployments: deployments.map(d => ({
          id: d.id,
          model: d.details.resources.backend_details.model.name,
          status: d.status,
        })),
      });
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Failed to refresh deployments:', err.message);
      res.status(500).json({
        error: {
          message: err.message || 'Failed to refresh deployments',
          type: 'api_error',
          param: null,
          code: '500',
        },
      });
    }
  });

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
