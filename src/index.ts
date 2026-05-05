import dotenv from 'dotenv';
import { loadConfig } from './config';
import { createApp } from './app';
import { logger } from './logger';

// Load environment variables
dotenv.config();

/**
 * SAP AI Core LLM Proxy - Entry Point
 * Provides OpenAI-compatible and Anthropic-native API endpoints backed by SAP AI Core
 */
async function main(): Promise<void> {
  // Load and validate configuration
  const config = loadConfig();

  // Create Express app with all routes
  const { app, deploymentManager } = createApp(config);

  // Pre-fetch deployments
  try {
    logger.info('Fetching available deployments...');
    const deployments = await deploymentManager.getDeployments();
    logger.info(`Found ${deployments.length} available model deployments`);
  } catch (error: unknown) {
    const err = error as { message?: string };
    logger.warn('Failed to pre-fetch deployments:', err.message);
    logger.warn('Deployments will be fetched on first request');
  }

  // Start server
  app.listen(config.port, '0.0.0.0', () => {
    logger.info(`SAP AI Core Proxy listening on 0.0.0.0:${config.port}`);
    logger.info('Endpoints:');
    logger.info(`  GET  /health - Health check`);
    logger.info('  --- OpenAI ---');
    logger.info(`  GET  /openai/v1/models - List available models`);
    logger.info(`  GET  /openai/v1/models/:modelId - Get model details`);
    logger.info(`  POST /openai/v1/chat/completions - Chat completion`);
    logger.info('  --- Anthropic ---');
    logger.info(`  GET  /anthropic/v1/models - List available models`);
    logger.info(`  POST /anthropic/v1/messages - Anthropic Messages API`);
    logger.info(`  POST /anthropic/v1/messages/count_tokens - Token counting`);
    logger.info('  --- Admin ---');
    logger.info(`  POST /admin/refresh-deployments - Refresh deployments cache`);
  });
}

// Start the proxy
main().catch(error => {
  logger.error('Failed to start proxy:', error);
  process.exit(1);
});
