import { Router, Request, Response } from 'express';
import { DeploymentManager } from '../sap-ai-core/deployments';
import { logger } from '../logger';

/**
 * Creates a router for admin endpoints.
 */
export function createAdminRouter(deploymentManager: DeploymentManager): Router {
  const router = Router();

  router.post('/refresh-deployments', async (_req: Request, res: Response) => {
    try {
      const deployments = await deploymentManager.refreshDeployments();
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
  });

  return router;
}
