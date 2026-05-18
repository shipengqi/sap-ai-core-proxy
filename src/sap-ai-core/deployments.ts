import { AuthManager } from './auth';
import { SapClient } from './client';
import { Deployment, DeploymentsResponse, ModelDeployment, DeploymentSummary } from './types';
import { logger } from '../logger';

/**
 * Deployment Manager for SAP AI Core
 * Handles fetching and caching of available model deployments
 */
export class DeploymentManager {
  private authManager: AuthManager;
  private client: SapClient;
  private deployments: Deployment[] = [];
  private lastFetchTime: number = 0;
  private cacheDuration: number = 60000; // 1 minute cache

  constructor(authManager: AuthManager) {
    this.authManager = authManager;
    this.client = new SapClient(authManager);
  }

  /**
   * Fetches deployments from SAP AI Core
   */
  private async fetchDeployments(): Promise<Deployment[]> {
    logger.debug('Fetching deployments from SAP AI Core');

    try {
      const response = await this.client.get<DeploymentsResponse>('/v2/lm/deployments?$top=10000&$skip=0');
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
      throw new Error(`Failed to fetch deployments: ${axiosError.message || 'Unknown error'}`, { cause: error });
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
  private async findDeploymentForModel(modelName: string): Promise<Deployment | undefined> {
    const deployments = await this.getDeployments();
    return deployments.find(d =>
      d.details.resources.backend_details.model.name === modelName
    );
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

  async refreshDeployments(): Promise<DeploymentSummary[]> {
    this.lastFetchTime = 0;
    const deployments = await this.getDeployments();
    return deployments.map(d => ({
      id: d.id,
      sapName: d.details.resources.backend_details.model.name,
      status: d.status,
    }));
  }

  async getDeploymentModels(): Promise<ModelDeployment[]> {
    const deployments = await this.getDeployments();
    return deployments.map(d => ({
      sapName: d.details.resources.backend_details.model.name,
      createdAt: d.createdAt,
    }));
  }

  async findModelDeployment(modelName: string): Promise<ModelDeployment | undefined> {
    const deployment = await this.findDeploymentForModel(modelName);
    if (!deployment) return undefined;
    return {
      sapName: deployment.details.resources.backend_details.model.name,
      createdAt: deployment.createdAt,
    };
  }
}
