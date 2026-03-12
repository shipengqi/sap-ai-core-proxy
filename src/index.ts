import express, { Request, Response, NextFunction } from 'express';
import dotenv from 'dotenv';
import { v4 as uuidv4 } from 'uuid';
import { AuthManager } from './auth';
import { DeploymentManager } from './deployments';
import { OpenAIHandler } from './handlers/openai';
import { AnthropicHandler } from './handlers/anthropic';
import { GeminiHandler } from './handlers/gemini';
import { logger } from './logger';
import { 
  SapAiCoreCredentials, 
  OpenAIChatCompletionRequest,
  OpenAIModelsResponse,
  OpenAIModel 
} from './types';

// Load environment variables
dotenv.config();

/**
 * SAP AI Core LLM Proxy
 * Provides OpenAI-compatible API endpoints backed by SAP AI Core
 */
class SapAiCoreProxy {
  private app: express.Application;
  private authManager: AuthManager;
  private deploymentManager: DeploymentManager;
  private openaiHandler: OpenAIHandler;
  private anthropicHandler: AnthropicHandler;
  private geminiHandler: GeminiHandler;
  private port: number;

  constructor() {
    this.app = express();
    this.port = parseInt(process.env.PORT || '3000', 10);

    // Initialize credentials from environment
    const credentials: SapAiCoreCredentials = {
      clientId: process.env.SAP_AI_CORE_CLIENT_ID || '',
      clientSecret: process.env.SAP_AI_CORE_CLIENT_SECRET || '',
      tokenUrl: process.env.SAP_AI_CORE_TOKEN_URL || '',
      baseUrl: process.env.SAP_AI_CORE_BASE_URL || '',
      resourceGroup: process.env.SAP_AI_CORE_RESOURCE_GROUP || 'default',
    };

    // Validate credentials
    this.validateCredentials(credentials);

    // Initialize managers and handlers
    this.authManager = new AuthManager(credentials);
    this.deploymentManager = new DeploymentManager(this.authManager);
    this.openaiHandler = new OpenAIHandler(this.authManager, this.deploymentManager);
    this.anthropicHandler = new AnthropicHandler(this.authManager, this.deploymentManager);
    this.geminiHandler = new GeminiHandler(this.authManager, this.deploymentManager);

    // Setup middleware and routes
    this.setupMiddleware();
    this.setupRoutes();
  }

  /**
   * Validates that required credentials are present
   */
  private validateCredentials(credentials: SapAiCoreCredentials): void {
    const missing: string[] = [];
    if (!credentials.clientId) missing.push('SAP_AI_CORE_CLIENT_ID');
    if (!credentials.clientSecret) missing.push('SAP_AI_CORE_CLIENT_SECRET');
    if (!credentials.tokenUrl) missing.push('SAP_AI_CORE_TOKEN_URL');
    if (!credentials.baseUrl) missing.push('SAP_AI_CORE_BASE_URL');

    if (missing.length > 0) {
      logger.error(`Missing required environment variables: ${missing.join(', ')}`);
      logger.info('Please set the following environment variables:');
      logger.info('  SAP_AI_CORE_CLIENT_ID - OAuth client ID');
      logger.info('  SAP_AI_CORE_CLIENT_SECRET - OAuth client secret');
      logger.info('  SAP_AI_CORE_TOKEN_URL - OAuth token URL');
      logger.info('  SAP_AI_CORE_BASE_URL - SAP AI Core API base URL');
      logger.info('  SAP_AI_CORE_RESOURCE_GROUP - Resource group (optional, default: "default")');
      process.exit(1);
    }
  }

  /**
   * Sets up Express middleware
   */
  private setupMiddleware(): void {
    // Parse JSON bodies
    this.app.use(express.json({ limit: '50mb' }));
    
    // Request logging
    this.app.use((req: Request, _res: Response, next: NextFunction) => {
      logger.request(req.method, req.path, req.body);
      next();
    });

    // CORS headers
    this.app.use((_req: Request, res: Response, next: NextFunction) => {
      res.setHeader('Access-Control-Allow-Origin', '*');
      res.setHeader('Access-Control-Allow-Methods', 'GET, POST, OPTIONS');
      res.setHeader('Access-Control-Allow-Headers', 'Content-Type, Authorization');
      next();
    });

    // Handle OPTIONS requests
    this.app.options('*', (_req: Request, res: Response) => {
      res.status(204).end();
    });
  }

  /**
   * Sets up API routes
   */
  private setupRoutes(): void {
    // Health check
    this.app.get('/health', (_req: Request, res: Response) => {
      res.json({ status: 'ok', timestamp: new Date().toISOString() });
    });

    // OpenAI-compatible endpoints
    this.app.get('/v1/models', this.handleListModels.bind(this));
    this.app.get('/models', this.handleListModels.bind(this));
    
    this.app.post('/v1/chat/completions', this.handleChatCompletions.bind(this));
    this.app.post('/chat/completions', this.handleChatCompletions.bind(this));

    // Additional endpoints for compatibility
    this.app.get('/v1/models/:modelId', this.handleGetModel.bind(this));

    // Refresh deployments endpoint
    this.app.post('/admin/refresh-deployments', this.handleRefreshDeployments.bind(this));

    // Error handling
    this.app.use((err: Error, _req: Request, res: Response, _next: NextFunction) => {
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
    this.app.use((_req: Request, res: Response) => {
      res.status(404).json({
        error: {
          message: 'Not found',
          type: 'invalid_request_error',
          param: null,
          code: '404',
        },
      });
    });
  }

  /**
   * Handles GET /v1/models - List available models
   */
  private async handleListModels(_req: Request, res: Response): Promise<void> {
    try {
      const deployments = await this.deploymentManager.getDeployments();
      
      const models: OpenAIModel[] = deployments.map(d => ({
        id: d.details.resources.backend_details.model.name,
        object: 'model' as const,
        created: new Date(d.createdAt).getTime() / 1000,
        owned_by: this.getModelOwner(d.details.resources.backend_details.model.name),
      }));

      const response: OpenAIModelsResponse = {
        object: 'list',
        data: models,
      };

      res.json(response);
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Failed to list models:', err.message);
      res.status(500).json({
        error: {
          message: err.message || 'Failed to list models',
          type: 'api_error',
          param: null,
          code: '500',
        },
      });
    }
  }

  /**
   * Handles GET /v1/models/:modelId - Get specific model
   */
  private async handleGetModel(req: Request, res: Response): Promise<void> {
    try {
      const { modelId } = req.params;
      const deployment = await this.deploymentManager.findDeploymentForModel(modelId);
      
      if (!deployment) {
        res.status(404).json({
          error: {
            message: `Model ${modelId} not found`,
            type: 'invalid_request_error',
            param: 'model',
            code: 'model_not_found',
          },
        });
        return;
      }

      const model: OpenAIModel = {
        id: deployment.details.resources.backend_details.model.name,
        object: 'model',
        created: new Date(deployment.createdAt).getTime() / 1000,
        owned_by: this.getModelOwner(deployment.details.resources.backend_details.model.name),
      };

      res.json(model);
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Failed to get model:', err.message);
      res.status(500).json({
        error: {
          message: err.message || 'Failed to get model',
          type: 'api_error',
          param: null,
          code: '500',
        },
      });
    }
  }

  /**
   * Handles POST /v1/chat/completions - Chat completion
   */
  private async handleChatCompletions(req: Request, res: Response): Promise<void> {
    const chatRequest = req.body as OpenAIChatCompletionRequest;

    // Validate request
    if (!chatRequest.model) {
      res.status(400).json({
        error: {
          message: 'Missing required parameter: model',
          type: 'invalid_request_error',
          param: 'model',
          code: 'missing_parameter',
        },
      });
      return;
    }

    if (!chatRequest.messages || !Array.isArray(chatRequest.messages) || chatRequest.messages.length === 0) {
      res.status(400).json({
        error: {
          message: 'Missing required parameter: messages',
          type: 'invalid_request_error',
          param: 'messages',
          code: 'missing_parameter',
        },
      });
      return;
    }

    try {
      // Determine which handler to use based on model provider
      const provider = this.deploymentManager.getModelProvider(chatRequest.model);
      
      logger.info(`Processing chat completion for model: ${chatRequest.model} (provider: ${provider})`);

      switch (provider) {
        case 'anthropic':
          await this.anthropicHandler.handleChatCompletion(chatRequest, res);
          break;
        case 'gemini':
          await this.geminiHandler.handleChatCompletion(chatRequest, res);
          break;
        case 'openai':
        case 'meta':
        case 'mistral':
        default:
          // Use OpenAI-compatible handler for most models
          await this.openaiHandler.handleChatCompletion(chatRequest, res);
          break;
      }
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.error('Chat completion failed:', err.message);
      
      if (!res.headersSent) {
        res.status(500).json({
          error: {
            message: err.message || 'Chat completion failed',
            type: 'api_error',
            param: null,
            code: '500',
          },
        });
      }
    }
  }

  /**
   * Handles POST /admin/refresh-deployments - Force refresh of deployments cache
   */
  private async handleRefreshDeployments(_req: Request, res: Response): Promise<void> {
    try {
      const deployments = await this.deploymentManager.refreshDeployments();
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
  }

  /**
   * Gets the owner/provider for a model
   */
  private getModelOwner(modelName: string): string {
    const lowerName = modelName.toLowerCase();
    if (lowerName.includes('gpt') || lowerName.includes('o1') || lowerName.includes('o3')) {
      return 'openai';
    }
    if (lowerName.includes('claude') || lowerName.includes('anthropic')) {
      return 'anthropic';
    }
    if (lowerName.includes('gemini')) {
      return 'google';
    }
    if (lowerName.includes('llama') || lowerName.includes('meta')) {
      return 'meta';
    }
    if (lowerName.includes('mistral') || lowerName.includes('mixtral')) {
      return 'mistral';
    }
    return 'sap-ai-core';
  }

  /**
   * Starts the proxy server
   */
  async start(): Promise<void> {
    // Pre-fetch deployments
    try {
      logger.info('Fetching available deployments...');
      const deployments = await this.deploymentManager.getDeployments();
      logger.info(`Found ${deployments.length} available model deployments`);
    } catch (error: unknown) {
      const err = error as { message?: string };
      logger.warn('Failed to pre-fetch deployments:', err.message);
      logger.warn('Deployments will be fetched on first request');
    }

    this.app.listen(this.port, '0.0.0.0', () => {
      logger.info(`SAP AI Core Proxy listening on 0.0.0.0:${this.port}`);
      logger.info('Endpoints:');
      logger.info(`  GET  /health - Health check`);
      logger.info(`  GET  /v1/models - List available models`);
      logger.info(`  POST /v1/chat/completions - Chat completion`);
      logger.info(`  POST /admin/refresh-deployments - Refresh deployments cache`);
    });
  }
}

// Start the proxy
const proxy = new SapAiCoreProxy();
proxy.start().catch(error => {
  logger.error('Failed to start proxy:', error);
  process.exit(1);
});