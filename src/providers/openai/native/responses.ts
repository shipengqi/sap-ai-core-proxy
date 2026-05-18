import { Request, Response } from 'express';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
import { setSSEHeaders, handleOpenAIError } from '../../../utils';
import { logger } from '../../../logger';

export class ResponsesProvider {
  private static readonly MAX_CACHE_SIZE = 10_000;
  private responseToDeployment = new Map<string, string>();

  private deploymentManager: DeploymentManager;
  private client: SapClient;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.deploymentManager = deploymentManager;
    this.client = new SapClient(authManager);
  }

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
      const path = `/v2/inference/deployments/${deploymentId}/responses`;

      logger.debug(`Responses create request: model=${body.model}, stream=${body.stream}`);

      if (body.stream) {
        const response = await this.client.postStream(path, body);
        if (response.status >= 400) {
          let errorBody = '';
          for await (const chunk of response.data) {
            errorBody += chunk.toString('utf-8');
          }
          handleOpenAIError({ response: { status: response.status, data: errorBody } }, res);
          return;
        }
        setSSEHeaders(res);
        response.data.pipe(res);
        response.data.on('error', (err: Error) => {
          logger.error('Responses stream error:', err.message);
          res.end();
        });
      } else {
        const response = await this.client.post(path, body);
        const responseId: string | undefined = (response.data as { id?: string }).id;
        if (responseId) {
          this.responseToDeployment.set(responseId, deploymentId);
          if (this.responseToDeployment.size > ResponsesProvider.MAX_CACHE_SIZE) {
            const oldest = this.responseToDeployment.keys().next().value!;
            this.responseToDeployment.delete(oldest);
          }
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
      const path = `/v2/inference/deployments/${deploymentId}/responses/${responseId}`;

      logger.debug(`Get response ${responseId}`);
      const response = await this.client.get(path);
      res.json(response.data);
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }

  async handleDelete(responseId: string, _req: Request, res: Response): Promise<void> {
    try {
      const deploymentId = await this.resolveDeployment(responseId);
      const path = `/v2/inference/deployments/${deploymentId}/responses/${responseId}`;

      logger.debug(`Delete response ${responseId}`);
      const response = await this.client.delete(path);
      this.responseToDeployment.delete(responseId);
      res.json(response.data);
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }

  private async resolveDeployment(responseId: string): Promise<string> {
    const cached = this.responseToDeployment.get(responseId);
    if (cached) return cached;
    throw Object.assign(new Error(`Response not found: ${responseId}`), { statusCode: 404 });
  }
}
