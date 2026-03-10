import axios from 'axios';
import { AuthManager } from './auth';
import { Deployment, DeploymentsResponse, ModelProvider, ModelInfo } from './types';
import { logger } from './logger';

/**
 * Model information mapping
 */
const MODEL_INFO: Record<string, ModelInfo> = {
  // OpenAI Models
  'gpt-4o': { provider: 'openai', maxTokens: 16384, contextWindow: 128000, supportsStreaming: true, supportsVision: true },
  'gpt-4o-mini': { provider: 'openai', maxTokens: 16384, contextWindow: 128000, supportsStreaming: true, supportsVision: true },
  'gpt-4': { provider: 'openai', maxTokens: 8192, contextWindow: 8192, supportsStreaming: true },
  'gpt-4.1': { provider: 'openai', maxTokens: 32768, contextWindow: 1047576, supportsStreaming: true, supportsVision: true },
  'gpt-4.1-nano': { provider: 'openai', maxTokens: 32768, contextWindow: 1047576, supportsStreaming: true, supportsVision: true },
  'gpt-5': { provider: 'openai', maxTokens: 100000, contextWindow: 1047576, supportsStreaming: true, supportsVision: true },
  'gpt-5-nano': { provider: 'openai', maxTokens: 100000, contextWindow: 1047576, supportsStreaming: true, supportsVision: true },
  'gpt-5-mini': { provider: 'openai', maxTokens: 100000, contextWindow: 1047576, supportsStreaming: true, supportsVision: true },
  'o1': { provider: 'openai', maxTokens: 100000, contextWindow: 200000, supportsStreaming: true },
  'o3-mini': { provider: 'openai', maxTokens: 100000, contextWindow: 200000, supportsStreaming: false },
  'o3': { provider: 'openai', maxTokens: 100000, contextWindow: 200000, supportsStreaming: true },
  'o4-mini': { provider: 'openai', maxTokens: 100000, contextWindow: 200000, supportsStreaming: true },
  
  // Anthropic Models
  'anthropic--claude-4.6-sonnet': { provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-4.5-sonnet': { provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-4.5-opus': { provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-4.5-haiku': { provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-4-sonnet': { provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-4-opus': { provider: 'anthropic', maxTokens: 16384, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-3.7-sonnet': { provider: 'anthropic', maxTokens: 8192, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-3.5-sonnet': { provider: 'anthropic', maxTokens: 8192, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-3-opus': { provider: 'anthropic', maxTokens: 4096, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-3-sonnet': { provider: 'anthropic', maxTokens: 4096, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  'anthropic--claude-3-haiku': { provider: 'anthropic', maxTokens: 4096, contextWindow: 200000, supportsStreaming: true, supportsVision: true },
  
  // Gemini Models
  'gemini-2.5-pro': { provider: 'gemini', maxTokens: 65536, contextWindow: 2097152, supportsStreaming: true, supportsVision: true },
  'gemini-2.5-flash': { provider: 'gemini', maxTokens: 65536, contextWindow: 1048576, supportsStreaming: true, supportsVision: true },
  'gemini-1.5-pro': { provider: 'gemini', maxTokens: 8192, contextWindow: 2097152, supportsStreaming: true, supportsVision: true },
  'gemini-1.5-flash': { provider: 'gemini', maxTokens: 8192, contextWindow: 1048576, supportsStreaming: true, supportsVision: true },
  
  // Perplexity Models
  'sonar-pro': { provider: 'openai', maxTokens: 8192, contextWindow: 200000, supportsStreaming: true },
  'sonar': { provider: 'openai', maxTokens: 8192, contextWindow: 127072, supportsStreaming: true },
  
  // Meta Models (Llama)
  'meta--llama3-70b-instruct': { provider: 'meta', maxTokens: 8192, contextWindow: 8192, supportsStreaming: true },
  'meta--llama3.1-70b-instruct': { provider: 'meta', maxTokens: 8192, contextWindow: 128000, supportsStreaming: true },
  
  // Mistral Models
  'mistralai--mixtral-8x7b-instruct-v01': { provider: 'mistral', maxTokens: 32768, contextWindow: 32768, supportsStreaming: true },
  'mistralai--mistral-large-instruct-2407': { provider: 'mistral', maxTokens: 32768, contextWindow: 128000, supportsStreaming: true },
};

/**
 * Deployment Manager for SAP AI Core
 * Handles fetching and caching of available model deployments
 */
export class DeploymentManager {
  private authManager: AuthManager;
  private deployments: Deployment[] = [];
  private lastFetchTime: number = 0;
  private cacheDuration: number = 60000; // 1 minute cache

  constructor(authManager: AuthManager) {
    this.authManager = authManager;
  }

  /**
   * Fetches deployments from SAP AI Core
   */
  private async fetchDeployments(): Promise<Deployment[]> {
    const headers = await this.authManager.buildHeaders();
    const baseUrl = this.authManager.getBaseUrl();
    const url = `${baseUrl}/v2/lm/deployments?$top=10000&$skip=0`;

    logger.debug(`Fetching deployments from ${url}`);

    try {
      const response = await axios.get<DeploymentsResponse>(url, { headers });
      const deployments = response.data.resources || [];

      // Filter for running deployments with valid model info
      const runningDeployments = deployments.filter((d: Deployment) => {
        const hasModel = d.details?.resources?.backend_details?.model?.name;
        const isRunning = d.status === 'RUNNING' || d.targetStatus === 'RUNNING';
        return hasModel && isRunning;
      });

      logger.info(`Found ${runningDeployments.length} running deployments`);
      
      runningDeployments.forEach((d: Deployment) => {
        const modelName = d.details.resources.backend_details.model.name;
        const modelVersion = d.details.resources.backend_details.model.version;
        logger.debug(`  - ${modelName}:${modelVersion} (${d.id})`);
      });

      return runningDeployments;
    } catch (error: unknown) {
      const axiosError = error as { response?: { status?: number; data?: unknown }; message?: string };
      logger.error('Failed to fetch deployments:', axiosError.response?.data || axiosError.message);
      throw new Error(`Failed to fetch deployments: ${axiosError.message || 'Unknown error'}`);
    }
  }

  /**
   * Gets deployments, using cache if valid
   */
  async getDeployments(): Promise<Deployment[]> {
    const now = Date.now();
    if (this.deployments.length > 0 && (now - this.lastFetchTime) < this.cacheDuration) {
      return this.deployments;
    }

    this.deployments = await this.fetchDeployments();
    this.lastFetchTime = now;
    return this.deployments;
  }

  /**
   * Finds a deployment for a given model name
   */
  async findDeploymentForModel(modelName: string): Promise<Deployment | undefined> {
    const deployments = await this.getDeployments();
    
    // Try exact match first
    let deployment = deployments.find(d => 
      d.details.resources.backend_details.model.name === modelName
    );

    // Try partial match (model name without version)
    if (!deployment) {
      const baseModelName = modelName.split(':')[0].toLowerCase();
      deployment = deployments.find(d => {
        const deploymentModelName = d.details.resources.backend_details.model.name.toLowerCase();
        return deploymentModelName === baseModelName || 
               deploymentModelName.includes(baseModelName) ||
               baseModelName.includes(deploymentModelName);
      });
    }

    return deployment;
  }

  /**
   * Gets deployment ID for a model
   */
  async getDeploymentId(modelName: string): Promise<string> {
    const deployment = await this.findDeploymentForModel(modelName);
    if (!deployment) {
      throw new Error(`No running deployment found for model: ${modelName}`);
    }
    return deployment.id;
  }

  /**
   * Gets model info for a given model name
   */
  getModelInfo(modelName: string): ModelInfo | undefined {
    // Direct lookup
    if (MODEL_INFO[modelName]) {
      return MODEL_INFO[modelName];
    }

    // Try to find by partial match
    const lowerModelName = modelName.toLowerCase();
    for (const [key, info] of Object.entries(MODEL_INFO)) {
      if (lowerModelName.includes(key.toLowerCase()) || key.toLowerCase().includes(lowerModelName)) {
        return info;
      }
    }

    return undefined;
  }

  /**
   * Determines the provider for a model
   */
  getModelProvider(modelName: string): ModelProvider {
    const info = this.getModelInfo(modelName);
    if (info) {
      return info.provider;
    }

    // Fallback detection based on model name
    const lowerName = modelName.toLowerCase();
    if (lowerName.includes('gpt') || lowerName.includes('o1') || lowerName.includes('o3')) {
      return 'openai';
    }
    if (lowerName.includes('claude') || lowerName.includes('anthropic')) {
      return 'anthropic';
    }
    if (lowerName.includes('gemini')) {
      return 'gemini';
    }
    if (lowerName.includes('llama') || lowerName.includes('meta')) {
      return 'meta';
    }
    if (lowerName.includes('mistral') || lowerName.includes('mixtral')) {
      return 'mistral';
    }

    // Default to OpenAI-compatible
    return 'openai';
  }

  /**
   * Forces refresh of deployments cache
   */
  async refreshDeployments(): Promise<Deployment[]> {
    this.lastFetchTime = 0;
    return this.getDeployments();
  }
}