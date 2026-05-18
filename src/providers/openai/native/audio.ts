import { Request, Response } from 'express';
import { AuthManager } from '../../../sap-ai-core/auth';
import { DeploymentManager } from '../../../sap-ai-core/deployments';
import { SapClient } from '../../../sap-ai-core/client';
import { handleOpenAIError } from '../../../utils';
import { logger } from '../../../logger';

export interface MulterRequest extends Request {
  file?: Express.Multer.File;
}

export class AudioProvider {
  private deploymentManager: DeploymentManager;
  private client: SapClient;

  constructor(authManager: AuthManager, deploymentManager: DeploymentManager) {
    this.deploymentManager = deploymentManager;
    this.client = new SapClient(authManager);
  }

  async handleTranscription(req: MulterRequest, res: Response): Promise<void> {
    if (!req.file) {
      res.status(400).json({
        error: {
          message: 'Missing required file upload',
          type: 'invalid_request_error',
          param: 'file',
          code: 'missing_parameter',
        },
      });
      return;
    }

    const model: string = (req.body as { model?: string }).model ?? 'whisper';

    try {
      const deploymentId = await this.deploymentManager.getDeploymentId(model);
      const path = `/v2/inference/deployments/${deploymentId}/audio/transcriptions?api-version=2024-06-01`;

      const form = new FormData();
      form.append('file', new Blob([new Uint8Array(req.file.buffer)], { type: req.file.mimetype }), req.file.originalname);
      for (const [key, value] of Object.entries(req.body as Record<string, string>)) {
        form.append(key, value);
      }

      logger.debug(`Audio transcription request: model=${model}`);
      const response = await this.client.postForm(path, form);
      res.json(response.data);
    } catch (error: unknown) {
      handleOpenAIError(error, res);
    }
  }
}
