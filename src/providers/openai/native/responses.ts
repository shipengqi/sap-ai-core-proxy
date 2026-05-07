import { Request, Response } from 'express';
import axios from 'axios';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { setSSEHeaders, handleOpenAIError } from '../../../utils';
import { logger } from '../../../logger';

export class ResponsesProvider {
  private responseToDeployment = new Map<string, string>();

  constructor(
    private authManager: AuthManager,
    private deploymentManager: DeploymentManager,
  ) {}

  async handleCreate(req: Request, res: Response): Promise<void> {
    const body = req.body as { model: string; input: unknown; stream?: boolean; [key: string]: unknown };

    if (!body.model) {
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

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(body.model);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();
      const url = `${baseUrl}/v2/inference/deployments/${deploymentId}/responses`;

      logger.debug(`Responses create request to ${url}`, { model: body.model, stream: body.stream });

      if (body.stream) {
        setSSEHeaders(res);
        const response = await axios.post(url, body, { headers, responseType: 'stream' });
        response.data.pipe(res);
        response.data.on('error', (err: Error) => {
          logger.error('Responses stream error:', err.message);
          res.end();
        });
      } else {
        const response = await axios.post(url, body, { headers });
        const responseId: string | undefined = (response.data as { id?: string }).id;
        if (responseId) {
          this.responseToDeployment.set(responseId, deploymentId);
        }
        res.json(response.data);
      }
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }

  async handleGet(responseId: string, _req: Request, res: Response): Promise<void> {
    try {
      const deploymentId = await this.resolveDeployment(responseId);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();
      const url = `${baseUrl}/v2/inference/deployments/${deploymentId}/responses/${responseId}`;

      logger.debug(`Get response ${responseId}`);
      const response = await axios.get(url, { headers });
      res.json(response.data);
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }

  async handleDelete(responseId: string, _req: Request, res: Response): Promise<void> {
    try {
      const deploymentId = await this.resolveDeployment(responseId);
      const baseUrl = this.authManager.getBaseUrl();
      const headers = await this.authManager.buildHeaders();
      const url = `${baseUrl}/v2/inference/deployments/${deploymentId}/responses/${responseId}`;

      logger.debug(`Delete response ${responseId}`);
      const response = await axios.delete(url, { headers });
      this.responseToDeployment.delete(responseId);
      res.json(response.data);
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }

  private async resolveDeployment(responseId: string): Promise<string> {
    const cached = this.responseToDeployment.get(responseId);
    if (cached) return cached;
    return this.deploymentManager.getDeploymentId('gpt-4o');
  }
}
