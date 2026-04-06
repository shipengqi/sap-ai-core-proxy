import { SapAiCoreCredentials } from './sap-ai-core/types';
import { logger } from './logger';

export interface AppConfig {
  port: number;
  credentials: SapAiCoreCredentials;
}

/**
 * Loads and validates configuration from environment variables
 */
export function loadConfig(): AppConfig {
  const credentials: SapAiCoreCredentials = {
    clientId: process.env.SAP_AI_CORE_CLIENT_ID || '',
    clientSecret: process.env.SAP_AI_CORE_CLIENT_SECRET || '',
    tokenUrl: process.env.SAP_AI_CORE_TOKEN_URL || '',
    baseUrl: process.env.SAP_AI_CORE_BASE_URL || '',
    resourceGroup: process.env.SAP_AI_CORE_RESOURCE_GROUP || 'default',
  };

  validateCredentials(credentials);

  return {
    port: parseInt(process.env.PORT || '3001', 10),
    credentials,
  };
}

/**
 * Validates that required credentials are present
 */
function validateCredentials(credentials: SapAiCoreCredentials): void {
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
