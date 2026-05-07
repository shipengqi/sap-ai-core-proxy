import { Request, Response } from 'express';
import axios from 'axios';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { handleOpenAIError } from '../../../utils';
import { logger } from '../../../logger';

export class EmbeddingsProvider {
  constructor(
    private authManager: AuthManager,
    private deploymentManager: DeploymentManager,
  ) {}

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
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();
      const url = `${baseUrl}/v2/inference/deployments/${deploymentId}/embeddings?api-version=2024-12-01-preview`;

      logger.debug(`Embeddings request to ${url}`, { model });
      const response = await axios.post(url, { model, input, ...rest }, { headers });
      res.json(response.data);
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }
}
