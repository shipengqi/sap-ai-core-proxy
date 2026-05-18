import { Request, Response } from 'express';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
import { handleOpenAIError } from '../../../utils';
import { logger } from '../../../logger';

export class EmbeddingsProvider {
  private deploymentManager: DeploymentManager;
  private client: SapClient;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.deploymentManager = deploymentManager;
    this.client = new SapClient(authManager);
  }

  async handleEmbeddings(req: Request, res: Response): Promise<void> {
    const { model, input, ...rest } = req.body as { model: string; input: string | string[]; [key: string]: unknown };

    if (!model || input === undefined) {
      res.status(400).json({
        error: {
          message: 'Missing required parameters: model, input',
          type: 'invalid_request_error',
          param: !model ? 'model' : 'input',
          code: 'missing_parameter',
        },
      });
      return;
    }

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const path = `/v2/inference/deployments/${deploymentId}/embeddings?api-version=2024-12-01-preview`;

      logger.debug(`Embeddings request: model=${model}`);
      const response = await this.client.post(path, { model, input, ...rest });
      res.json(response.data);
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }
}
